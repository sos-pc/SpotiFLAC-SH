package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Admin-only maintenance operations
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
	"github.com/sos-pc/SpotiFLAC-SH/backend/providerutil"
	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/applog"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"github.com/sos-pc/SpotiFLAC-SH/internal/m3u8"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
	"github.com/sos-pc/SpotiFLAC-SH/internal/watcher"
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

// libraryRebuildTimeout is a last-resort circuit breaker for a genuinely
// wedged scan (e.g. a hung network mount) — not a budget for legitimate
// work. It used to be the ONE deadline shared across every file in the
// walk, which meant a large-but-healthy library could have its very last
// file cut off by "context deadline exceeded" simply because the shared
// clock ran out — observed in practice on a clean 1800-file scan. Real
// per-file protection now comes from libraryRebuildPerFileTimeout below;
// this one is deliberately generous so it should never fire for legitimate
// use, no matter the library size.
const libraryRebuildTimeout = 2 * time.Hour

// libraryRebuildPerFileTimeout bounds a single file's catalog writes
// (ingestLibraryFile). A slow or wedged file only ever costs itself —
// counted in result.Failed, the walk moves on to the next file — instead
// of eating into a budget every other file also needs.
const libraryRebuildPerFileTimeout = 30 * time.Second

// registerAdminRoutes wires the /api/v1/admin/* endpoints. Each handler must
// guard with v1RequireAdmin since the dispatch layer only checks authentication.
func (s *Server) registerAdminRoutes() {
	s.mux.Handle("POST /api/v1/admin/retag-legacy", s.v1Auth(s.v1RetagLegacy))
	s.mux.Handle("POST /api/v1/admin/library-rebuild", s.v1Auth(s.v1LibraryRebuild))
	s.mux.Handle("POST /api/v1/admin/retag-incomplete-metadata", s.v1Auth(s.v1RetagIncompleteMetadata))
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
	writeV1JSON(w, http.StatusOK, applog.ServerLogs.Snapshot())
}

// v1RetagLegacy walks every Done/Skipped job in BoltDB whose FilePath still
// exists on disk, and writes its SPOTIFY_ID into the file's tags if missing.
// Used once after deploying the tag-embedding change to retro-fit files
// downloaded before this commit. Idempotent: existing matching tags are skipped.
func (s *Server) v1RetagLegacy(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}

	jobList, err := s.ctr.Jobs.GetAllJobs()
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeV1JSON(w, http.StatusOK, retagJobs(jobList))
}

// retagJobs retags every Done/Skipped job in jobs whose FilePath still
// exists on disk. Shared by v1RetagLegacy (all jobs) and the per-watchlist
// repair endpoint (jobs pre-filtered to one watchlist).
func retagJobs(jobList []jobs.Job) retagLegacyResult {
	result := retagLegacyResult{}
	for _, job := range jobList {
		if job.SpotifyID == "" || job.FilePath == "" {
			continue
		}
		if job.Status != jobs.StatusDone && job.Status != jobs.StatusSkipped {
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
//
// The walk can take many minutes on a large library — well past any
// reverse-proxy's read timeout, which in production silently cancelled
// the in-flight scan the moment the browser/proxy gave up on the
// connection (r.Context() cancellation propagates straight into the
// walk). Like v1RepairWatchlist, this handler only validates inputs
// inline and returns 202 immediately; the actual walk runs in the
// background against a fresh context and announces completion over SSE
// (library_rebuild_done).
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

	util.SafeGo("admin.library-rebuild", func() { s.runLibraryRebuildAsync(roots) })
	writeV1JSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// runLibraryRebuildAsync performs the actual walk in the background — see
// v1LibraryRebuild for why this can't run inline in the request.
func (s *Server) runLibraryRebuildAsync(roots []string) {
	slog.Info("[Catalog] library-rebuild: starting", "roots", roots)

	ctx, cancel := context.WithTimeout(context.Background(), libraryRebuildTimeout)
	defer cancel()

	result := libraryRebuildResult{ScanRoots: roots}
	seenIDs := make(map[string]bool)
	for _, root := range roots {
		s.scanRootForRebuild(ctx, root, &result, seenIDs)
	}
	if ctx.Err() != nil {
		result.TimedOut = true
	}

	slog.Info("[Catalog] library-rebuild: done", "files_scanned", result.FilesScanned,
		"imported", result.Imported, "verified", result.Verified, "moved", result.Moved,
		"duplicate", result.Duplicate, "no_tag", result.NoTag, "failed", result.Failed, "timed_out", result.TimedOut)

	if s.ctr.Jobs != nil {
		s.ctr.Jobs.Publish(jobs.JobEvent{
			Type: "library_rebuild_done",
			Data: result,
		})
	}
}

// watchlistRepairResult is the JSON payload returned by
// POST /api/v1/watchlists/{id}/repair.
type watchlistRepairResult struct {
	Retag     retagLegacyResult     `json:"retag"`
	Rebuild   libraryRebuildResult  `json:"rebuild"`
	M3U8      m3u8.GenerationResult `json:"m3u8"`
	M3U8Error string                `json:"m3u8_error,omitempty"`
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
// The rebuild step can take a while on a large library — well past any
// reverse-proxy's read timeout — so the handler only kicks off the work and
// returns 202 immediately; completion is announced over SSE
// (watchlist_repaired), mirroring how syncPlaylist announces
// watchlist_synced.
func (s *Server) v1RepairWatchlist(w http.ResponseWriter, r *http.Request) {
	if !v1RequirePermission(w, r, "manage") {
		return
	}
	id := r.PathValue("id")
	user := auth.GetUserFromContext(r)
	if err := s.checkWatchlistOwnership(id, user); err != nil {
		writeV1Error(w, http.StatusForbidden, err.Error())
		return
	}

	pl, err := s.ctr.Watcher.GetWatchlistByID(id)
	if err != nil || pl == nil {
		writeV1Error(w, http.StatusNotFound, "watchlist not found")
		return
	}

	util.SafeGo("watchlist.repair["+id+"]", func() { s.runWatchlistRepair(*pl) })

	writeV1JSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// runWatchlistRepair performs the actual repair steps in the background —
// see v1RepairWatchlist for why this can't run inline in the request.
func (s *Server) runWatchlistRepair(pl watcher.WatchedPlaylist) {
	slog.Info("[Repair] starting", "playlist", pl.Name)
	result := watchlistRepairResult{}

	// 1. Retag this watchlist's own legacy files (BoltDB job record still
	// present, file on disk, but no SPOTIFY_ID tag yet).
	if jobList, jobsErr := s.ctr.Jobs.GetAllJobs(); jobsErr == nil {
		scoped := make([]jobs.Job, 0, len(jobList))
		for _, j := range jobList {
			if j.WatchlistID == pl.ID {
				scoped = append(scoped, j)
			}
		}
		result.Retag = retagJobs(scoped)
		slog.Info("[Repair] retag done", "playlist", pl.Name,
			"scanned", result.Retag.Scanned, "tagged", result.Retag.Tagged, "skipped", result.Retag.Skipped, "failed", result.Retag.Failed)
	} else {
		slog.Warn("[Repair] failed to list jobList for retag", "playlist", pl.Name, "err", jobsErr)
	}

	// 2. Rebuild the catalog for this watchlist owner's download path only
	// (not every root — this is a scoped, per-playlist repair). Watchlists
	// follow the owner's global settings now, so that's where their files land.
	if s.ctr.Catalog != nil {
		root := settings.EffectiveDownloadSettings(s.ctr.Auth, pl.UserID).DownloadPath
		if root == "" {
			root = util.GetDefaultMusicPath()
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
			slog.Info("[Repair] rebuild done", "playlist", pl.Name,
				"scanned", rebuildResult.FilesScanned, "imported", rebuildResult.Imported, "verified", rebuildResult.Verified,
				"moved", rebuildResult.Moved, "duplicate", rebuildResult.Duplicate, "no_tag", rebuildResult.NoTag,
				"failed", rebuildResult.Failed, "timed_out", rebuildResult.TimedOut)
		} else {
			slog.Warn("[Repair] download path not accessible, skipping rebuild", "playlist", pl.Name, "path", root, "err", statErr)
		}
	}

	// 3. Force-regenerate the M3U8 with whatever the catalog/retag work
	// above just improved.
	m3uResult, m3uErr := s.ctr.Watcher.GenerateM3U8ForPlaylist(pl.ID, true)
	result.M3U8 = m3uResult
	if m3uErr != nil {
		result.M3U8Error = m3uErr.Error()
		slog.Warn("[Repair] M3U8 regeneration failed", "playlist", pl.Name, "err", m3uErr)
	}
	slog.Info("[Repair] done", "playlist", pl.Name, "resolved", result.M3U8.Resolved, "total", result.M3U8.Total)

	if s.ctr.Jobs != nil {
		s.ctr.Jobs.Publish(jobs.JobEvent{
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
				dp := settings.EffectiveDownloadSettings(s.ctr.Auth, pl.UserID).DownloadPath
				if dp == "" {
					continue
				}
				if !isSubPath(globalRoot, dp) {
					slog.Warn("[Admin] library-rebuild: ignoring watchlist download path outside library root", "path", dp)
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
			slog.Info("[Catalog] library-rebuild: still walking", "root", root,
				"files_scanned", result.FilesScanned, "imported", result.Imported, "verified", result.Verified,
				"moved", result.Moved, "duplicate", result.Duplicate, "no_tag", result.NoTag, "failed", result.Failed)
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

		// Derived from ctx (the overall safety-net deadline), not a fresh
		// background context — so cancelling the whole rebuild (client
		// disconnect, the 2h backstop) still stops an in-flight file's
		// writes immediately instead of waiting out this shorter timeout.
		fileCtx, cancel := context.WithTimeout(ctx, libraryRebuildPerFileTimeout)
		bucket, err := s.ingestLibraryFile(fileCtx, tags.SpotifyID, tags, path)
		cancel()
		if err != nil {
			slog.Warn("[Catalog] library-rebuild: ingest failed", "path", path, "err", err)
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
	if err := db.UpsertTrack(ctx, s.ctr.Catalog, jobs.CatalogTrackFromTags(spotifyID, tags)); err != nil {
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

// ─────────────────────────────────────────────────────────────────────────────
// retag-incomplete-metadata — backfill missing catalog/tag fields on
// already-present files, without re-downloading audio.
//
// library-rebuild only recovers what's ALREADY embedded in a file's own
// tags; a file that was downloaded before a given field was embedded (or by
// a provider that never had it, like ISRC) stays incomplete forever no
// matter how many times it's rescanned. This pass instead re-fetches the
// track's own metadata — a lightweight Spotify lookup (the same source
// used for every download, just re-run per track instead of per playlist)
// plus the existing ISRC/genre lookup (backend/isrclookup, then
// MusicBrainz) already used at real download time — and fills in ONLY the
// fields the file/catalog is currently missing, exactly like
// WriteSpotifyIDTag/applyTrackOverrides do elsewhere: an already-present
// value, file or catalog, is never overwritten.
// ─────────────────────────────────────────────────────────────────────────────

// retagIncompleteMetadataResult is the JSON payload returned by
// POST /api/v1/admin/retag-incomplete-metadata.
type retagIncompleteMetadataResult struct {
	Scanned   int      `json:"scanned"`
	Filled    int      `json:"filled"`  // at least one field was written (file and/or catalog)
	Skipped   int      `json:"skipped"` // fresh metadata had nothing new to offer
	Failed    int      `json:"failed"`
	FailedIDs []string `json:"failed_ids,omitempty"`

	// Genre diagnostics. This pass selects a track when ANY of twelve fields
	// is empty, genre among them — and genre is the one field an external
	// service may simply not have. A run reports scanned=2556 filled=17,
	// re-selects the same 2534 next time, and never converges (R10).
	//
	// "Skipped" alone cannot say why, and the three causes below call for
	// three different fixes: an unresolved ISRC is our bug, a failing source
	// is our bug, but a genre nobody has is a fact about the world — no
	// widening of the source chain will ever fill it, and only dropping genre
	// from the selection clause (or remembering we tried) makes the set
	// shrink. These counters are what tells them apart.
	GenreBySource map[string]int `json:"genre_by_source,omitempty"` // tier -> tracks it filled
	GenreNoISRC   int            `json:"genre_no_isrc"`             // never asked: no ISRC resolved
	GenreUnknown  int            `json:"genre_unknown"`             // asked, nobody had a genre
	GenreFailed   int            `json:"genre_failed"`              // every source errored
	GenreAlready  int            `json:"genre_already"`             // already tagged, nothing to do

	// Why each track was SELECTED — which empty field tripped the clause
	// (backend/db/tracks.go). The genre chain fills genre, but a track can be
	// re-selected forever on a DIFFERENT empty field one that no source
	// supplies (e.g. copyright), which no amount of genre work touches. The
	// first run measured genre coverage; this measures what actually keeps the
	// set from converging, so the fix targets the real blocker instead of a
	// guessed one. A track missing several fields increments several counters
	// — this is "how often is each field the/a reason", not a partition.
	SelectedByField map[string]int `json:"selected_by_field,omitempty"`
}

// genreIsMissing reports whether t.Genre should be treated as absent for
// selection/retry purposes. util.UnknownGenre counts as missing exactly like
// "": it records that every source was asked and none knew this recording,
// not that the field is settled — a source's catalog can gain the data
// later, so it must keep being re-attempted (see GetTracksNeedingRetag's
// matching SQL condition and retagOneTrack's skip-guard, both of which must
// stay in sync with this).
func genreIsMissing(genre string) bool {
	return genre == "" || genre == util.UnknownGenre
}

// countSelectionReason records which clause fields were empty on a selected
// track. Mirrors the WHERE in GetTracksNeedingRetag exactly; if that clause
// changes, this must change with it.
func (r *retagIncompleteMetadataResult) countSelectionReason(t db.Track) {
	if r.SelectedByField == nil {
		r.SelectedByField = make(map[string]int)
	}
	bump := func(name string, empty bool) {
		if empty {
			r.SelectedByField[name]++
		}
	}
	bump("isrc", t.ISRC == "")
	bump("name", t.Name == "")
	bump("artist_name", t.ArtistName == "")
	bump("track_number", t.TrackNumber == 0)
	bump("disc_number", t.DiscNumber == 0)
	bump("duration_ms", t.DurationMs == 0)
	bump("genre", genreIsMissing(t.Genre))
	bump("release_date", t.ReleaseDate == "")
	bump("album_name", t.AlbumName == "")
	bump("album_artist", t.AlbumArtist == "")
	bump("cover_url", t.CoverURL == "")
	bump("copyright", t.Copyright == "")
}

// countGenre files one track's genre outcome into the right bucket. Kept next
// to the counters it feeds so the two cannot drift apart.
func (r *retagIncompleteMetadataResult) countGenre(d genreDiag) {
	if !d.asked {
		// The chain was never reached (the track failed earlier, or already
		// had a genre) — not evidence about genre coverage either way.
		r.GenreAlready++
		return
	}
	switch d.outcome {
	case meta.GenreFound:
		if r.GenreBySource == nil {
			r.GenreBySource = make(map[string]int)
		}
		r.GenreBySource[d.source]++
	case meta.GenreNoISRC:
		r.GenreNoISRC++
	case meta.GenreFailed:
		r.GenreFailed++
	default: // meta.GenreUnknown
		r.GenreUnknown++
	}
}

// retagIncompleteMetadataThrottle paces the external Spotify/Deezer/
// MusicBrainz calls this pass makes — one track at a time, a short pause
// between each, so a library needing thousands of backfills doesn't hammer
// those shared services.
const retagIncompleteMetadataThrottle = 1 * time.Second

// retagIncompleteMetadataPerTrackTimeout bounds a single track's Spotify +
// ISRC/genre lookups + writes. Same reasoning as libraryRebuildPerFileTimeout:
// one slow or wedged track only ever costs itself, never the rest of the run.
const retagIncompleteMetadataPerTrackTimeout = 30 * time.Second

// v1RetagIncompleteMetadata used to run synchronously like v1LibraryRebuild
// (its sibling maintenance endpoint), but the same failure mode hit it: a
// library with many incomplete tracks needs one throttled external-API
// round trip per track, easily running well past a reverse-proxy's read
// timeout — the moment the client/proxy gave up on the connection,
// r.Context() cancellation killed the whole in-flight pass. Same fix as
// library-rebuild: validate inputs inline, return 202 immediately, run the
// actual work in the background against a fresh context, announce
// completion over SSE (retag_incomplete_metadata_done).
func (s *Server) v1RetagIncompleteMetadata(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	if s.ctr.Catalog == nil {
		writeV1Error(w, http.StatusServiceUnavailable, "catalog database not available")
		return
	}

	tracks, err := db.GetTracksNeedingRetag(r.Context(), s.ctr.Catalog)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	util.SafeGo("admin.retag-incomplete-metadata", func() { s.runRetagIncompleteMetadataAsync(tracks) })
	writeV1JSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// runRetagIncompleteMetadataAsync performs the actual retag pass in the
// background — see v1RetagIncompleteMetadata for why this can't run inline
// in the request.
func (s *Server) runRetagIncompleteMetadataAsync(tracks []db.TrackForRetag) {
	result := s.retagIncompleteMetadata(context.Background(), tracks)
	if s.ctr.Jobs != nil {
		s.ctr.Jobs.Publish(jobs.JobEvent{
			Type: "retag_incomplete_metadata_done",
			Data: result,
		})
	}
}

func (s *Server) retagIncompleteMetadata(ctx context.Context, tracks []db.TrackForRetag) retagIncompleteMetadataResult {
	result := retagIncompleteMetadataResult{}
	lastLog := time.Now()
	slog.Info("[Retag] incomplete-metadata: starting", "tracks", len(tracks))

	for i, t := range tracks {
		if ctx.Err() != nil {
			break
		}
		result.Scanned++
		// Recorded from the pre-retag row: this is the state that tripped the
		// selection clause, which is the question here (why was it picked),
		// not what it looks like afterwards.
		result.countSelectionReason(t.Track)
		if time.Since(lastLog) >= scanProgressLogInterval {
			slog.Info("[Retag] incomplete-metadata: still working", "processed", i, "total", len(tracks),
				"filled", result.Filled, "skipped", result.Skipped, "failed", result.Failed)
			lastLog = time.Now()
		}

		trackCtx, cancel := context.WithTimeout(ctx, retagIncompleteMetadataPerTrackTimeout)
		filled, diag, err := s.retagOneTrack(trackCtx, t)
		cancel()
		switch {
		case err != nil:
			slog.Warn("[Retag] incomplete-metadata: track failed", "spotify_id", t.SpotifyID, "err", err)
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, t.SpotifyID)
		case filled:
			result.Filled++
		default:
			result.Skipped++
		}

		// Counted regardless of whether the track was filled: a genre can be
		// found while every other field was already complete, and a genre can
		// be missing on a track filled from Spotify alone. Genre coverage and
		// "did we write anything" are different questions.
		result.countGenre(diag)

		if i < len(tracks)-1 && ctx.Err() == nil {
			select {
			case <-ctx.Done():
			case <-time.After(retagIncompleteMetadataThrottle):
			}
		}
	}

	slog.Info("[Retag] incomplete-metadata done",
		"scanned", result.Scanned, "filled", result.Filled, "skipped", result.Skipped, "failed", result.Failed)
	// Logged apart from the line above because it answers a different
	// question: not "did the pass work" but "why does genre stay empty" — the
	// one that decides whether R10 needs the selection clause changed.
	slog.Info("[Retag] incomplete-metadata genre breakdown",
		"by_source", result.GenreBySource,
		"nobody_knows", result.GenreUnknown,
		"no_isrc", result.GenreNoISRC,
		"all_sources_failed", result.GenreFailed,
		"not_asked", result.GenreAlready)
	// Which empty field selected each track — the counter that says whether
	// dropping genre from the clause would actually help, or whether another
	// field (that no source fills) keeps the set from converging.
	slog.Info("[Retag] incomplete-metadata selection reasons",
		"by_field", result.SelectedByField)
	return result
}

// genreDiag records what the genre chain did for one track, so the pass's
// summary can separate outcomes that all look like "skipped" from outside.
// Zero value means the track already had a genre and we never asked.
type genreDiag struct {
	asked   bool
	source  string
	outcome meta.GenreOutcome
}

// retagOneTrack re-fetches t's Spotify/ISRC/genre metadata and fills in
// whatever the file's tags and the catalog row are currently missing.
// Returns whether anything was actually written, plus what the genre chain
// did (see genreDiag — the pass counts these to explain R10).
func (s *Server) retagOneTrack(ctx context.Context, t db.TrackForRetag) (bool, genreDiag, error) {
	var diag genreDiag

	if _, statErr := os.Stat(t.FilePath); statErr != nil {
		return false, diag, fmt.Errorf("file no longer on disk: %w", statErr)
	}
	current := meta.ReadFullTrackTags(t.FilePath)

	data, err := spotify.GetFilteredSpotifyData(ctx, "spotify:track:"+t.SpotifyID, false, 0)
	if err != nil {
		return false, diag, fmt.Errorf("fetch spotify metadata: %w", err)
	}
	spotifyTracks := watcher.ExtractTracksFromMetadata(data)
	if len(spotifyTracks) == 0 {
		return false, diag, fmt.Errorf("spotify returned no metadata for this track (removed or region-locked?)")
	}
	jt := spotifyTracks[0]

	// This lookup does double duty (see providerutil.FetchGenreMetadataAsync):
	// it resolves the ISRC from the track URL AND walks the genre chain
	// (Apple -> Deezer -> MusicBrainz) with it. useSingleGenre=true: a
	// maintenance pass has no per-user setting to consult, so it defaults to
	// the simpler single-genre form.
	//
	// Skip it entirely when the catalog already holds both an ISRC and a
	// real genre, and reuse those values to backfill the file if its tags
	// are missing them. Measured: a second pass re-resolved 211 genres/ISRCs
	// it already had, every run, for tracks selected on some OTHER empty
	// field — pure repeated network cost. Because it is skipped, diag stays
	// asked=false, counted as "already".
	//
	// util.UnknownGenre does NOT count as "already holds a real genre" —
	// see genreIsMissing — so a track we previously gave up on still gets
	// retried here every pass, exactly like a track with a blank genre.
	trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", t.SpotifyID)
	var freshISRC, freshGenre string
	if t.ISRC != "" && !genreIsMissing(t.Genre) {
		freshISRC = t.ISRC
		freshGenre = t.Genre
	} else {
		select {
		case mb := <-providerutil.FetchGenreMetadataAsync("", trackURL, jt.TrackName, jt.ArtistName, jt.AlbumName, true, true):
			freshISRC = mb.ISRC
			freshGenre = mb.Metadata.Genre
			diag = genreDiag{asked: true, source: mb.Source, outcome: mb.Outcome}
		case <-ctx.Done():
			return false, diag, ctx.Err()
		}
	}

	fresh := meta.FullTrackTags{
		Title:       jt.TrackName,
		Artist:      jt.ArtistName,
		Album:       jt.AlbumName,
		AlbumArtist: jt.AlbumArtist,
		ReleaseDate: jt.ReleaseDate,
		TrackNumber: jt.TrackNumber,
		DiscNumber:  jt.DiscNumber,
		Copyright:   jt.Copyright,
		ISRC:        freshISRC,
		Genre:       freshGenre,
	}

	catalogTrack := t.Track // copy — start from the existing row, only override what's fresh
	jobs.ApplyTrackOverrides(&catalogTrack, jobs.TrackOverrides{
		Name:        jt.TrackName,
		ArtistName:  jt.ArtistName,
		AlbumName:   jt.AlbumName,
		AlbumArtist: jt.AlbumArtist,
		ReleaseDate: jt.ReleaseDate,
		CoverURL:    jt.CoverURL,
		Copyright:   jt.Copyright,
		TrackNumber: jt.TrackNumber,
		DiscNumber:  jt.DiscNumber,
		DurationMs:  jt.DurationMs,
	})
	if freshISRC != "" {
		catalogTrack.ISRC = freshISRC
	}
	if freshGenre != "" {
		catalogTrack.Genre = freshGenre
	}
	if err := db.UpsertTrack(ctx, s.ctr.Catalog, &catalogTrack); err != nil {
		return false, diag, fmt.Errorf("upsert track: %w", err)
	}

	written, err := meta.WriteMissingTags(t.FilePath, current, fresh)
	if err != nil {
		return written, diag, fmt.Errorf("write tags: %w", err)
	}
	return written, diag, nil
}
