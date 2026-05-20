package main

// ─────────────────────────────────────────────────────────────────────────────
// Catalog mirroring for terminal job transitions.
//
// On every done/failed/skipped, the worker calls one of the three
// recordCatalog* helpers below to mirror the event into SQLite. All three
// are best-effort: errors are logged with a [Catalog] prefix but never
// propagated to the caller. The BoltDB job remains the user-facing source
// of truth; the catalog is the long-term audit trail.
//
// Album metadata is not yet linked: Job carries AlbumName/AlbumArtist as
// strings but no AlbumSpotifyID, so we leave Track.AlbumID empty for now.
// A later commit will plumb the album ID through and enable UpsertAlbum.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
)

// catalogWriteTimeout caps how long any single catalog mirror operation
// blocks the worker. The catalog is a local SQLite file so writes should
// be fast; the timeout exists to bound pathological cases (disk full, fs
// stalls) and avoid wedging the queue.
const catalogWriteTimeout = 5 * time.Second

// recordCatalogDone writes the success state of a job to the catalog:
// upsert the track, insert (or replace) the library_file, and append a
// done DownloadAttempt linked to it.
//
// If a non-deleted library_file already exists for this Spotify ID:
//   - Same FilePath: keep it, just record the attempt.
//   - Different FilePath: mark old as deleted (audit), insert new, link
//     the attempt to the new ID.
func (jm *JobManager) recordCatalogDone(j *Job) {
	if jm.catalog == nil || j == nil || j.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		fmt.Printf("[Catalog] UpsertTrack failed for %s: %v\n", j.SpotifyID, err)
		return
	}

	libraryFileID, err := upsertActiveLibraryFile(ctx, jm.catalog, j)
	if err != nil {
		fmt.Printf("[Catalog] library_file write failed for %s: %v\n", j.SpotifyID, err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusDone)
	attempt.LibraryFileID = libraryFileID
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		fmt.Printf("[Catalog] CreateDownloadAttempt(done) failed for %s: %v\n", j.SpotifyID, err)
	}
}

// recordCatalogFailed writes a failed attempt: upsert the track (the
// metadata is still useful for catalog history) and append a failed
// DownloadAttempt with the error message.
func (jm *JobManager) recordCatalogFailed(j *Job) {
	if jm.catalog == nil || j == nil || j.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		fmt.Printf("[Catalog] UpsertTrack failed for %s: %v\n", j.SpotifyID, err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusFailed)
	attempt.Error = j.Error
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		fmt.Printf("[Catalog] CreateDownloadAttempt(failed) failed for %s: %v\n", j.SpotifyID, err)
	}
}

// recordCatalogSkipped writes a skipped attempt. We do NOT create a
// library_file row from the worker's "file already exists" path: we don't
// know which provider/quality produced the existing file. The library
// rebuild endpoint (next commit) will ingest such orphan files via the
// SPOTIFY_ID tag scan.
func (jm *JobManager) recordCatalogSkipped(j *Job) {
	if jm.catalog == nil || j == nil || j.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		fmt.Printf("[Catalog] UpsertTrack failed for %s: %v\n", j.SpotifyID, err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusSkipped)
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		fmt.Printf("[Catalog] CreateDownloadAttempt(skipped) failed for %s: %v\n", j.SpotifyID, err)
	}
}

// jobToCatalogTrack projects the Spotify-side identity fields of a Job
// into a catalog Track. AlbumID is left empty until the job carries the
// Spotify album ID (separate commit).
func jobToCatalogTrack(j *Job) *db.Track {
	return &db.Track{
		SpotifyID:   j.SpotifyID,
		Name:        j.TrackName,
		ArtistName:  j.ArtistName,
		TrackNumber: j.TrackNumber,
		DiscNumber:  j.DiscNumber,
		DurationMs:  j.DurationMs,
	}
}

// jobToCatalogAttempt builds a DownloadAttempt skeleton with the common
// per-attempt fields. Caller is expected to fill Error/LibraryFileID
// depending on the terminal status.
func jobToCatalogAttempt(j *Job, status string) *db.DownloadAttempt {
	return &db.DownloadAttempt{
		SpotifyID:   j.SpotifyID,
		UserID:      j.UserID,
		WatchlistID: j.WatchlistID,
		BatchID:     j.BatchID,
		Provider:    j.Settings.Service,
		Quality:     deriveCatalogQuality(j.Settings),
		Status:      status,
	}
}

// upsertActiveLibraryFile mirrors a successful download into library_files.
// Returns the resulting library_file ID (newly created or kept).
//
// If an existing active row already matches FilePath exactly, no write
// happens and the existing ID is returned. Otherwise the existing row
// (if any) is marked deleted (audit trail) and a fresh row is inserted.
func upsertActiveLibraryFile(ctx context.Context, q db.Querier, j *Job) (string, error) {
	existing, err := db.GetActiveLibraryFile(ctx, q, j.SpotifyID)
	if err != nil {
		return "", fmt.Errorf("get active library_file: %w", err)
	}
	if existing != nil && existing.FilePath == j.FilePath {
		return existing.ID, nil
	}
	if existing != nil {
		if err := db.MarkLibraryFileDeleted(ctx, q, existing.ID); err != nil {
			return "", fmt.Errorf("mark previous deleted: %w", err)
		}
	}

	lf := &db.LibraryFile{
		SpotifyID:    j.SpotifyID,
		Provider:     j.Settings.Service,
		Quality:      deriveCatalogQuality(j.Settings),
		Format:       fileExtension(j.FilePath),
		FilePath:     j.FilePath,
		FileSize:     fileSizeBytes(j.FilePath, j.TotalSize),
		DownloadedBy: j.UserID,
	}
	if err := db.CreateLibraryFile(ctx, q, lf); err != nil {
		return "", fmt.Errorf("create library_file: %w", err)
	}
	return lf.ID, nil
}

// deriveCatalogQuality maps JobSettings to a catalog quality string.
// The mapping is per-provider because Tidal/Qobuz/Amazon/Deezer each use
// their own naming conventions in settings; the catalog stores a single
// canonical vocabulary (LOSSLESS / HI_RES / HI_RES_LOSSLESS / HIGH).
//
// For "auto", we return what the user asked for via autoQuality; the
// actual provider that won is not surfaced by the current downloader
// response, so the recorded quality reflects intent rather than truth.
func deriveCatalogQuality(s JobSettings) string {
	switch s.Service {
	case "tidal":
		if s.TidalQuality != "" {
			return s.TidalQuality
		}
		return db.QualityLossless
	case "qobuz":
		switch s.QobuzQuality {
		case "27":
			return db.QualityHiResLossless
		case "7":
			return db.QualityHiRes
		default:
			return db.QualityLossless
		}
	case "amazon", "deezer":
		return db.QualityLossless
	case "auto":
		if s.AutoQuality == "24" {
			return db.QualityHiResLossless
		}
		return db.QualityLossless
	}
	return db.QualityLossless
}

// fileExtension returns the lowercase extension without the leading dot,
// or empty string if the path has no extension.
func fileExtension(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	return strings.TrimPrefix(ext, ".")
}

// fileSizeBytes returns the size of path in bytes via os.Stat. Falls back
// to fallbackMB * 1024 * 1024 if Stat fails (e.g. file already moved out).
func fileSizeBytes(path string, fallbackMB float64) int64 {
	if info, err := os.Stat(path); err == nil {
		return info.Size()
	}
	return int64(fallbackMB * 1024 * 1024)
}
