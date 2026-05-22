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

	writeV1JSON(w, http.StatusOK, result)
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

	writeV1JSON(w, http.StatusOK, result)
}

// collectScanRoots returns the deduplicated list of download paths to
// walk: union of every watchlist's Settings.DownloadPath plus the
// global default downloadPath. Non-existent or unreadable paths are
// silently dropped.
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

	if s.ctr.Watcher != nil {
		if pls, err := s.ctr.Watcher.GetWatchlists(); err == nil {
			for _, pl := range pls {
				addIfReadable(pl.Settings.DownloadPath)
			}
		}
	}
	if settings, _ := s.app.LoadSettings(); settings != nil {
		if path, _ := settings["downloadPath"].(string); path != "" {
			addIfReadable(path)
		}
	}
	return out
}

// scanRootForRebuild walks one filesystem root and ingests every audio
// file it finds into the catalog. Best-effort: per-file errors are
// counted in result.Failed and the walk continues.
func (s *Server) scanRootForRebuild(
	ctx context.Context, root string,
	result *libraryRebuildResult, seenIDs map[string]bool,
) {
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

		spotifyID, err := meta.ReadSpotifyID(path)
		if err != nil {
			fmt.Printf("[Catalog] library-rebuild: read tag %s -> %v\n", path, err)
			result.Failed++
			return nil
		}
		if spotifyID == "" {
			result.NoTag++
			if len(result.NoTagSample) < noTagSampleLimit {
				result.NoTagSample = append(result.NoTagSample, path)
			}
			return nil
		}
		if seenIDs[spotifyID] {
			result.Duplicate++
			return nil
		}
		seenIDs[spotifyID] = true

		bucket, err := s.ingestLibraryFile(ctx, spotifyID, path)
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
		}
		return nil
	})
}

// ingestBucket categorises what ingestLibraryFile did to the catalog so
// the handler can increment the right counter without inspecting DAO
// internals.
type ingestBucket int

const (
	ingestImported ingestBucket = iota // brand new library_file row
	ingestVerified                     // existing row at same path, last_verified_at bumped
	ingestMoved                        // existing row updated to a new path
)

// ingestLibraryFile makes the catalog reflect a tagged file at path:
//   - Track stub upserted (FK requirement).
//   - If no active library_file: create one (imported).
//   - If active row at same path: bump last_verified_at (verified).
//   - If active row at a different path: update path (moved).
func (s *Server) ingestLibraryFile(
	ctx context.Context, spotifyID, path string,
) (ingestBucket, error) {
	if err := db.UpsertTrackStub(ctx, s.ctr.Catalog, spotifyID); err != nil {
		return ingestImported, fmt.Errorf("upsert track stub: %w", err)
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
