package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Admin-only maintenance operations
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// retagLegacyResult is the JSON payload returned by POST /api/v1/admin/retag-legacy.
type retagLegacyResult struct {
	Scanned   int      `json:"scanned"`
	Tagged    int      `json:"tagged"`
	Skipped   int      `json:"skipped"`
	Failed    int      `json:"failed"`
	FailedIDs []string `json:"failed_ids,omitempty"`
}

// libraryRebuildResult is the JSON payload returned by
// POST /api/v1/admin/library-rebuild. Counters are mutually exclusive
// per file (each scanned audio file goes into exactly one bucket).
type libraryRebuildResult struct {
	ScanRoots    []string `json:"scan_roots"`              // filesystem roots that were walked
	FilesScanned int      `json:"files_scanned"`           // total audio files seen
	Imported     int      `json:"imported"`                // new library_file rows created
	Verified     int      `json:"verified"`                // existing rows refreshed (same path)
	Moved        int      `json:"moved"`                   // existing rows updated to a new path
	Duplicate    int      `json:"duplicate"`               // SPOTIFY_ID already seen earlier in the scan
	NoTag        int      `json:"no_tag"`                  // files without SPOTIFY_ID — candidates for library-match
	Failed       int      `json:"failed"`                  // catalog write errors (see logs)
	NoTagSample  []string `json:"no_tag_sample,omitempty"` // first N orphan paths to help the user
	// TimedOut is true if libraryRebuildTimeout was hit before every scan
	// root finished walking — the counters above only reflect what was
	// scanned before the cutoff, not the whole library. Re-run to continue
	// (already-ingested files are skipped fast on the next pass).
	TimedOut bool `json:"timed_out,omitempty"`
}

// noTagSampleLimit caps the number of orphan paths returned in the
// response. The full list could be tens of thousands of strings on a
// large library; the sample is enough for the user to verify the scan
// found their music tree without bloating the JSON payload.
const noTagSampleLimit = 50

// libraryRebuildTimeout caps the total walk + write time. A library of
// ~10 000 FLAC files is fully scanned in well under a minute on a
// reasonable disk, so 10 minutes is a generous safety net for very
// large or slow filesystems.
const libraryRebuildTimeout = 10 * time.Minute

// registerAdminRoutes wires the /api/v1/admin/* endpoints. Each handler must
// guard with v1RequireAdmin since the dispatch layer only checks authentication.
func (s *Server) registerAdminRoutes() {
	s.mux.Handle("POST /api/v1/admin/retag-legacy", s.v1Auth(s.v1RetagLegacy))
	s.mux.Handle("POST /api/v1/admin/library-rebuild", s.v1Auth(s.v1LibraryRebuild))
	s.mux.Handle("GET /api/v1/admin/logs", s.v1Auth(s.v1GetServerLogs))
}

// v1GetServerLogs returns the current in-memory backend log buffer — the
// same lines visible via `docker logs`, used by the Debug Logs page for its
// initial snapshot (live updates then arrive over SSE as server_log events).
// Admin-only: backend log lines can mention other users' watchlist names,
// file paths, and error details.
func (s *Server) v1GetServerLogs(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	writeV1JSON(w, http.StatusOK, serverLogs.snapshot())
}

// v1RetagLegacy walks every Done/Skipped job in BoltDB whose FilePath still
// exists on disk, and writes its SPOTIFY_ID into the file's tags if missing.
// Used once after deploying the tag-embedding change to retro-fit files
// downloaded before this commit. Idempotent: existing matching tags are skipped.
func (s *Server) v1RetagLegacy(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}

	jobs, err := s.ctr.Jobs.GetAllJobs()
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeV1JSON(w, http.StatusOK, retagJobs(jobs))
}

// retagJobs retags every Done/Skipped job in jobs whose FilePath still
// exists on disk. Shared by v1RetagLegacy (all jobs) and the per-watchlist
// repair endpoint (jobs pre-filtered to one watchlist).
func retagJobs(jobs []Job) retagLegacyResult {
	result := retagLegacyResult{}
	for _, job := range jobs {
		if job.SpotifyID == "" || job.FilePath == "" {
			continue
		}
		if job.Status != StatusDone && job.Status != StatusSkipped {
			continue
		}
		if _, statErr := os.Stat(job.FilePath); statErr != nil {
			continue
		}
		result.Scanned++
		written, writeErr := meta.WriteSpotifyIDTag(job.FilePath, job.SpotifyID)
		switch {
		case writeErr != nil:
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, job.SpotifyID)
		case written:
			result.Tagged++
		default:
			result.Skipped++
		}
	}
	return result
}

// v1LibraryRebuild walks every configured download path, reads the
// SPOTIFY_ID tag from each audio file, and ingests the result into the
// SQLite catalog. Counters in the response separate truly new entries
// from refreshed/moved ones so the operator can see at a glance what
// the rebuild changed.
//
// Files without a SPOTIFY_ID tag are reported as "no_tag" (with a small
// sample list) — those are the candidates for the upcoming
// /admin/library-match endpoint that will fuzzy-match them against
// Spotify metadata.
//
// Idempotent: re-running on a stable library bumps last_verified_at on
// every existing row and adds nothing.
func (s *Server) v1LibraryRebuild(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	if s.ctr.Catalog == nil {
		writeV1Error(w, http.StatusServiceUnavailable, "catalog database not available")
		return
	}

	roots := s.collectScanRoots()
	if len(roots) == 0 {
		writeV1Error(w, http.StatusBadRequest,
			"no scan roots: configure a watchlist with downloadPath or set downloadPath in settings")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), libraryRebuildTimeout)
	defer cancel()

	result := libraryRebuildResult{ScanRoots: roots}
	seenIDs := make(map[string]bool)
	for _, root := range roots {
		s.scanRootForRebuild(ctx, root, &result, seenIDs)
	}
	if ctx.Err() != nil {
		result.TimedOut = true
	}

	writeV1JSON(w, http.StatusOK, result)
}

// watchlistRepairResult is the JSON payload returned by
// POST /api/v1/watchlists/{id}/repair.
type watchlistRepairResult struct {
	Retag     retagLegacyResult    `json:"retag"`
	Rebuild   libraryRebuildResult `json:"rebuild"`
	M3U8      m3u8GenerationResult `json:"m3u8"`
	M3U8Error string               `json:"m3u8_error,omitempty"`
}

// v1RepairWatchlist runs the same recovery steps as retag-legacy +
// library-rebuild, scoped to a single watchlist's own download path and
// jobs, then force-regenerates that watchlist's M3U8 (bypassing the
// shrink-guard — an explicit repair should show the true current state,
// even if it's smaller than what's on disk now).
//
// Exists because retag-legacy/library-rebuild are admin-only, global,
// curl-only maintenance actions with no UI — this gives a watchlist owner
// (or an admin) a single "fix this playlist" action reachable from the
// watchlist card, for the exact class of "M3U8 lost most of its tracks"
// problem those two admin endpoints were built to address.
//
// The rebuild step can take up to libraryRebuildTimeout (10 min) on a large
// library, well past any reverse-proxy's read timeout, so the handler only
// kicks off the work and returns 202 immediately; completion is announced
// over SSE (watchlist_repaired), mirroring how syncPlaylist announces
// watchlist_synced.
func (s *Server) v1RepairWatchlist(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := GetUserFromContext(r)
	if err := s.checkWatchlistOwnership(id, user); err != nil {
		writeV1Error(w, http.StatusForbidden, err.Error())
		return
	}

	pl, err := s.ctr.Watcher.getWatchlistByID(id)
	if err != nil || pl == nil {
		writeV1Error(w, http.StatusNotFound, "watchlist not found")
		return
	}

	util.SafeGo("watchlist.repair["+id+"]", func() { s.runWatchlistRepair(*pl) })

	writeV1JSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// runWatchlistRepair performs the actual repair steps in the background —
// see v1RepairWatchlist for why this can't run inline in the request.
func (s *Server) runWatchlistRepair(pl WatchedPlaylist) {
	fmt.Printf("[Repair] %s: starting\n", pl.Name)
	result := watchlistRepairResult{}

	// 1. Retag this watchlist's own legacy files (BoltDB job record still
	// present, file on disk, but no SPOTIFY_ID tag yet).
	if jobs, jobsErr := s.ctr.Jobs.GetAllJobs(); jobsErr == nil {
		scoped := make([]Job, 0, len(jobs))
		for _, j := range jobs {
			if j.WatchlistID == pl.ID {
				scoped = append(scoped, j)
			}
		}
		result.Retag = retagJobs(scoped)
		fmt.Printf("[Repair] %s: retag scanned=%d tagged=%d skipped=%d failed=%d\n",
			pl.Name, result.Retag.Scanned, result.Retag.Tagged, result.Retag.Skipped, result.Retag.Failed)
	} else {
		fmt.Printf("[Repair] %s: failed to list jobs for retag: %v\n", pl.Name, jobsErr)
	}

	// 2. Rebuild the catalog for this watchlist's own download path only
	// (not every root — this is a scoped, per-playlist repair).
	if s.ctr.Catalog != nil {
		root := pl.Settings.DownloadPath
		if root == "" {
			root = "/home/nonroot/Music"
		}
		if _, statErr := os.Stat(root); statErr == nil {
			rebuildResult := libraryRebuildResult{ScanRoots: []string{root}}
			seenIDs := make(map[string]bool)
			ctx, cancel := context.WithTimeout(context.Background(), libraryRebuildTimeout)
			s.scanRootForRebuild(ctx, root, &rebuildResult, seenIDs)
			if ctx.Err() != nil {
				rebuildResult.TimedOut = true
			}
			cancel()
			result.Rebuild = rebuildResult
			fmt.Printf("[Repair] %s: rebuild scanned=%d imported=%d verified=%d moved=%d duplicate=%d no_tag=%d failed=%d timed_out=%v\n",
				pl.Name, rebuildResult.FilesScanned, rebuildResult.Imported, rebuildResult.Verified,
				rebuildResult.Moved, rebuildResult.Duplicate, rebuildResult.NoTag, rebuildResult.Failed, rebuildResult.TimedOut)
		} else {
			fmt.Printf("[Repair] %s: download path %q not accessible, skipping rebuild: %v\n", pl.Name, root, statErr)
		}
	}

	// 3. Force-regenerate the M3U8 with whatever the catalog/retag work
	// above just improved.
	m3uResult, m3uErr := s.ctr.Watcher.generateM3U8ForPlaylist(pl.ID, true)
	result.M3U8 = m3uResult
	if m3uErr != nil {
		result.M3U8Error = m3uErr.Error()
		fmt.Printf("[Repair] %s: M3U8 regeneration failed: %v\n", pl.Name, m3uErr)
	}
	fmt.Printf("[Repair] %s: done — %d/%d tracks resolved in M3U8\n", pl.Name, result.M3U8.Resolved, result.M3U8.Total)

	if s.ctr.Jobs != nil && s.ctr.Jobs.hub != nil {
		s.ctr.Jobs.hub.publish(JobEvent{
			Type: "watchlist_repaired",
			Data: map[string]interface{}{
				"watchlist_id": pl.ID,
				"name":         pl.Name,
				"result":       result,
			},
		})
	}
}

// collectScanRoots returns the deduplicated list of download paths to
// walk: the library root (s.libraryRoot() — the configured downloadPath, or
// util.GetDefaultMusicPath() if unset), plus every watchlist's
// Settings.DownloadPath that falls under it. Non-existent or unreadable
// paths are silently dropped.
//
// Watchlist DownloadPath is attacker-influenceable: creating/updating a
// watchlist (POST/PUT /api/v1/watchlists) is not admin-gated, so any
// authenticated non-admin user could otherwise set it to "/", "/etc", a
// slow network mount, etc. and redirect this admin-only maintenance walk
// anywhere on the host. Only trust a watchlist's path when it's contained
// within the admin-configured library root.
func (s *Server) collectScanRoots() []string {
	seen := make(map[string]bool)
	out := make([]string, 0)

	addIfReadable := func(path string) {
		if path == "" || seen[path] {
			return
		}
		if _, err := os.Stat(path); err != nil {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	globalRoot := s.libraryRoot()
	addIfReadable(globalRoot)

	if s.ctr.Watcher != nil {
		if pls, err := s.ctr.Watcher.GetWatchlists(); err == nil {
			for _, pl := range pls {
				dp := pl.Settings.DownloadPath
				if dp == "" {
					continue
				}
				if !isSubPath(globalRoot, dp) {
					fmt.Printf("[Admin] library-rebuild: ignoring watchlist download path outside library root: %s\n", dp)
					continue
				}
				addIfReadable(dp)
			}
		}
	}
	return out
}

// scanRootForRebuild walks one filesystem root and ingests every audio
// file it finds into the catalog. Best-effort: per-file errors are
// counted in result.Failed and the walk continues.
// scanProgressLogInterval bounds how often scanRootForRebuild prints a
// progress line during a long walk. Without this, a multi-thousand-file
// library scan is completely silent between the "starting" and "done"
// log lines — indistinguishable from a hang from the operator's point of
// view (see the "Repair looks stuck" reports on large libraries).
const scanProgressLogInterval = 5 * time.Second

func (s *Server) scanRootForRebuild(
	ctx context.Context, root string,
	result *libraryRebuildResult, seenIDs map[string]bool,
) {
	lastLog := time.Now()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return nil
		}
		if d.IsDir() || !meta.IsSupportedAudioExt(filepath.Ext(path)) {
			return nil
		}
		result.FilesScanned++
		if time.Since(lastLog) >= scanProgressLogInterval {
			fmt.Printf("[Catalog] library-rebuild: %s — %d files scanned so far (imported=%d verified=%d moved=%d duplicate=%d no_tag=%d failed=%d), still walking...\n",
				root, result.FilesScanned, result.Imported, result.Verified, result.Moved, result.Duplicate, result.NoTag, result.Failed)
			lastLog = time.Now()
		}

		// One read for everything: identification (SpotifyID) plus every
		// other tag ingestLibraryFile can backfill into the catalog.
		// Reading SpotifyID alone here and re-parsing the file again inside
		// ingestLibraryFile would double the FLAC-parse cost of this scan
		// across every file in the library.
		tags := meta.ReadFullTrackTags(path)
		if tags.SpotifyID == "" {
			result.NoTag++
			if len(result.NoTagSample) < noTagSampleLimit {
				result.NoTagSample = append(result.NoTagSample, path)
			}
			return nil
		}
		if seenIDs[tags.SpotifyID] {
			result.Duplicate++
			return nil
		}
		seenIDs[tags.SpotifyID] = true

		bucket, err := s.ingestLibraryFile(ctx, tags.SpotifyID, tags, path)
		if err != nil {
			fmt.Printf("[Catalog] library-rebuild: ingest %s -> %v\n", path, err)
			result.Failed++
			return nil
		}
		switch bucket {
		case ingestImported:
			result.Imported++
		case ingestVerified:
			result.Verified++
		case ingestMoved:
			result.Moved++
		case ingestDuplicate:
			result.Duplicate++
		}
		return nil
	})
}

// ingestBucket categorises what ingestLibraryFile did to the catalog so
// the handler can increment the right counter without inspecting DAO
// internals.
type ingestBucket int

const (
	ingestImported  ingestBucket = iota // brand new library_file row
	ingestVerified                      // existing row at same path, last_verified_at bumped
	ingestMoved                         // existing row updated to a new path
	ingestDuplicate                     // existing row's path is still valid; this file is a stale/duplicate copy, catalog untouched
)

// ingestLibraryFile makes the catalog reflect a tagged file at path:
//   - Track stub upserted (FK requirement).
//   - If no active library_file: create one (imported).
//   - If active row at same path: bump last_verified_at (verified).
//   - If active row at a different path that no longer exists on disk:
//     the file was genuinely moved — update the path.
//   - If active row at a different path that STILL exists on disk: path is
//     almost certainly a stale duplicate/orphan (e.g. left behind by a
//     re-download to a new folder-template path) rather than the real
//     current copy — counted as a duplicate, catalog is left untouched.
//
// This distinction matters because scanRootForRebuild visits files in
// filesystem walk order (not by recency) and dedups by SPOTIFY_ID, so
// whichever copy the walk happens to reach first would otherwise silently
// overwrite the catalog's active row — including regressing it FROM the
// correct current file TO a stale orphan purely because of directory
// traversal order, breaking M3U8 resolution for a track that was actually
// fine before the rebuild ran.
func (s *Server) ingestLibraryFile(
	ctx context.Context, spotifyID string, tags meta.FullTrackTags, path string,
) (ingestBucket, error) {
	// Full UpsertTrack (not just a stub) regardless of which bucket this
	// file lands in below — a file rediscovered at the SAME path
	// (ingestVerified) or a stale duplicate (ingestDuplicate) still
	// deserves its tracks row backfilled from tags on every rebuild run,
	// not just the first time it's ever seen.
	if err := db.UpsertTrack(ctx, s.ctr.Catalog, catalogTrackFromTags(spotifyID, tags)); err != nil {
		return ingestImported, fmt.Errorf("upsert track: %w", err)
	}

	existing, err := db.GetActiveLibraryFile(ctx, s.ctr.Catalog, spotifyID)
	if err != nil {
		return ingestImported, fmt.Errorf("get active library_file: %w", err)
	}
	if existing != nil && existing.FilePath == path {
		if err := db.UpdateLibraryFileStatus(ctx, s.ctr.Catalog, existing.ID, db.StatusPresent); err != nil {
			return ingestVerified, fmt.Errorf("verify existing: %w", err)
		}
		return ingestVerified, nil
	}
	if existing != nil {
		if _, statErr := os.Stat(existing.FilePath); statErr == nil {
			// Current catalog path is still valid — path is a duplicate,
			// not a move. Do not touch the catalog.
			return ingestDuplicate, nil
		}
		if err := db.UpdateLibraryFilePath(ctx, s.ctr.Catalog, existing.ID, path); err != nil {
			return ingestMoved, fmt.Errorf("update path: %w", err)
		}
		return ingestMoved, nil
	}

	ext := strings.ToLower(filepath.Ext(path))
	lf := &db.LibraryFile{
		SpotifyID: spotifyID,
		Provider:  catalogProviderUnknown,
		Quality:   defaultQualityForExt(ext),
		Format:    strings.TrimPrefix(ext, "."),
		FilePath:  path,
		FileSize:  fileSizeOrZero(path),
	}
	if err := db.CreateLibraryFile(ctx, s.ctr.Catalog, lf); err != nil {
		return ingestImported, fmt.Errorf("create library_file: %w", err)
	}
	return ingestImported, nil
}

// catalogProviderUnknown marks a library_file ingested from filesystem
// scan rather than from a download. The file's actual provider cannot be
// inferred from its tags alone; the value gets corrected on the next
// successful re-download (recordCatalogDone replaces the row).
const catalogProviderUnknown = "unknown"

// defaultQualityForExt returns a conservative catalog quality string for
// a file whose actual quality is unknown. Conservative = the lowest tier
// the format reasonably represents, so a future download in higher tier
// is allowed by checkCatalogDedup (existing rank < requested rank).
func defaultQualityForExt(ext string) string {
	switch ext {
	case ".flac", ".m4a":
		return db.QualityLossless
	case ".mp3":
		return db.QualityHigh
	}
	return db.QualityHigh
}

// fileSizeOrZero returns os.Stat size in bytes, or 0 on error. The
// catalog tolerates 0 as "unknown size"; the field is informational.
func fileSizeOrZero(path string) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return 0
}
