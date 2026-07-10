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

	"github.com/afkarxyz/SpotiFLAC/backend/audio"
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
		// Same path as the current active row — most likely a quality
		// upgrade that resolved to the same templated filename (the naming
		// template doesn't encode bitrate). Refresh quality/provider/size
		// instead of silently keeping the stale values from the original
		// download, otherwise checkCatalogDedup would think the upgrade
		// never happened and re-trigger it on every future sync.
		newQuality := actualCatalogQuality(j.FilePath, deriveCatalogQuality(j.Settings))
		newSize := fileSizeBytes(j.FilePath, j.TotalSize)
		if err := db.UpdateLibraryFileQuality(ctx, q, existing.ID, j.Settings.Service, newQuality, newSize); err != nil {
			return "", fmt.Errorf("refresh existing library_file: %w", err)
		}
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
		Quality:      actualCatalogQuality(j.FilePath, deriveCatalogQuality(j.Settings)),
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

// actualCatalogQuality inspects the downloaded file's real bit depth and
// sample rate via ffprobe and maps them to the catalog's quality
// vocabulary, falling back to fallback (typically deriveCatalogQuality's
// requested-quality result) when the file can't be analyzed.
//
// deriveCatalogQuality alone reflects what was REQUESTED, not what was
// actually delivered — a provider can silently downgrade (e.g. Tidal's
// AllowFallback quietly serving LOSSLESS when HI_RES_LOSSLESS isn't
// available for a track). Recording the request as if it were guaranteed
// would let checkCatalogDedup believe a track already satisfies a quality
// bar it doesn't actually meet, permanently blocking a legitimate future
// re-download that would have delivered the real thing.
func actualCatalogQuality(filePath, fallback string) string {
	result, err := audio.GetTrackMetadata(filePath)
	if err != nil || result == nil {
		return fallback
	}
	switch {
	case result.BitsPerSample >= 24 && result.SampleRate >= 96000:
		return db.QualityHiResLossless
	case result.BitsPerSample >= 24:
		return db.QualityHiRes
	case result.BitsPerSample >= 16:
		return db.QualityLossless
	case result.Bitrate > 0:
		return db.QualityHigh
	default:
		return fallback
	}
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


// catalogReadTimeout caps how long the dedup lookup blocks the enqueue
// path. Smaller than catalogWriteTimeout because we want EnqueueBatch
// fast: callers can wait on a download but not on a 200-track batch
// validation.
const catalogReadTimeout = 2 * time.Second

// catalogDedupResult tells EnqueueBatch how to treat a track based on
// the catalog state. Returned by checkCatalogDedup.
type catalogDedupResult struct {
	skip          bool
	libraryFileID string
	reason        string
}

// checkCatalogDedup decides whether a track should bypass enqueue based
// on what the catalog already knows. Best-effort: any error or missing
// catalog returns skip=false, so the queue still picks up the work.
//
// Skip rules (all must hold):
//   - jm.catalog is set.
//   - There is an active (non-deleted) library_file for this Spotify ID.
//   - The file actually still exists on disk (stat succeeds).
//   - Its quality_rank is greater than or equal to the requested one.
//
// The third check is critical: a stale catalog row pointing at a deleted
// file must NOT block a legitimate re-download. The fourth ensures users
// who upgrade their `autoQuality` to 24-bit can re-download even if the
// catalog already has a 16-bit copy.
func (jm *JobManager) checkCatalogDedup(spotifyID string, settings JobSettings) catalogDedupResult {
	if jm.catalog == nil || spotifyID == "" {
		return catalogDedupResult{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogReadTimeout)
	defer cancel()

	existing, err := db.GetActiveLibraryFile(ctx, jm.catalog, spotifyID)
	if err != nil {
		fmt.Printf("[Catalog] dedup lookup failed for %s: %v\n", spotifyID, err)
		return catalogDedupResult{}
	}
	if existing == nil {
		return catalogDedupResult{}
	}
	if _, err := os.Stat(existing.FilePath); err != nil {
		// Stale row — file was removed outside SpotiFLAC. Let the worker
		// re-download; a separate rebuild path will eventually clean up
		// the catalog row.
		return catalogDedupResult{}
	}

	requestedRank := db.QualityRank(deriveCatalogQuality(settings))
	if existing.QualityRank < requestedRank {
		return catalogDedupResult{}
	}

	return catalogDedupResult{
		skip:          true,
		libraryFileID: existing.ID,
		reason: fmt.Sprintf("already in library at %s (rank %d) ≥ requested rank %d",
			existing.Quality, existing.QualityRank, requestedRank),
	}
}

// recordCatalogDedupSkip writes a skipped DownloadAttempt for a track
// that was deduped at enqueue time. No Job ever reached the queue, so
// the caller passes the JobTrack and EnqueueBatchRequest directly.
//
// BatchID is intentionally left empty: these skips are not part of any
// real batch (no worker did any work for them) and counting them in the
// batch totals would inflate sync stats.
func (jm *JobManager) recordCatalogDedupSkip(track JobTrack, req EnqueueBatchRequest, libraryFileID, reason string) {
	if jm.catalog == nil || track.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, &db.Track{
		SpotifyID:   track.SpotifyID,
		Name:        track.TrackName,
		ArtistName:  track.ArtistName,
		TrackNumber: track.TrackNumber,
		DiscNumber:  track.DiscNumber,
		DurationMs:  track.DurationMs,
	}); err != nil {
		fmt.Printf("[Catalog] UpsertTrack failed for dedup-skip %s: %v\n", track.SpotifyID, err)
		return
	}

	attempt := &db.DownloadAttempt{
		SpotifyID:     track.SpotifyID,
		LibraryFileID: libraryFileID,
		UserID:        req.UserID,
		WatchlistID:   req.WatchlistID,
		Provider:      req.Settings.Service,
		Quality:       deriveCatalogQuality(req.Settings),
		Status:        db.AttemptStatusSkipped,
		Error:         reason,
	}
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		fmt.Printf("[Catalog] CreateDownloadAttempt(dedup-skip) failed for %s: %v\n", track.SpotifyID, err)
	}
}
