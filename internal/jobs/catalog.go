package jobs

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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/audio"
	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
)

// CatalogWriteTimeout caps how long any single catalog mirror operation
// blocks the worker. The catalog is a local SQLite file so writes should
// be fast; the timeout exists to bound pathological cases (disk full, fs
// stalls) and avoid wedging the queue.
const CatalogWriteTimeout = 5 * time.Second

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
	ctx, cancel := context.WithTimeout(context.Background(), CatalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		slog.Warn("[Catalog] UpsertTrack failed", "spotify_id", j.SpotifyID, "err", err)
		return
	}

	libraryFileID, err := upsertActiveLibraryFile(ctx, jm.catalog, j)
	if err != nil {
		slog.Warn("[Catalog] library_file write failed", "spotify_id", j.SpotifyID, "err", err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusDone)
	attempt.LibraryFileID = libraryFileID
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		slog.Warn("[Catalog] CreateDownloadAttempt(done) failed", "spotify_id", j.SpotifyID, "err", err)
	}
}

// recordCatalogFailed writes a failed attempt: upsert the track (the
// metadata is still useful for catalog history) and append a failed
// DownloadAttempt with the error message.
func (jm *JobManager) recordCatalogFailed(j *Job) {
	if jm.catalog == nil || j == nil || j.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), CatalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		slog.Warn("[Catalog] UpsertTrack failed", "spotify_id", j.SpotifyID, "err", err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusFailed)
	attempt.Error = j.Error
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		slog.Warn("[Catalog] CreateDownloadAttempt(failed) failed", "spotify_id", j.SpotifyID, "err", err)
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
	ctx, cancel := context.WithTimeout(context.Background(), CatalogWriteTimeout)
	defer cancel()

	if err := db.UpsertTrack(ctx, jm.catalog, jobToCatalogTrack(j)); err != nil {
		slog.Warn("[Catalog] UpsertTrack failed", "spotify_id", j.SpotifyID, "err", err)
		return
	}

	attempt := jobToCatalogAttempt(j, db.AttemptStatusSkipped)
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		slog.Warn("[Catalog] CreateDownloadAttempt(skipped) failed", "spotify_id", j.SpotifyID, "err", err)
	}
}

// CatalogTrackFromTags projects an already-read meta.FullTrackTags into a
// catalog Track. Pure/no I/O — the single place that maps tag field names
// to db.Track field names, so every caller that ends up with a
// FullTrackTags (from a fresh read, or reused to avoid a second parse of
// the same file) builds a Track the same way.
//
// AlbumID (the real Spotify album ID, for a proper albums-table link) is
// left empty — that needs threading through five different JSON payload
// shapes in watcher.go plus the manual single-track download path, a much
// larger change; ReleaseDate/AlbumName/AlbumArtist/Copyright are
// denormalized straight onto the track row instead (see migration 0005).
// CoverURL has no tag-embedded source (cover art is a binary picture
// block, not a fetchable URL) — only ever set by callers with a Job.
func CatalogTrackFromTags(spotifyID string, tags meta.FullTrackTags) *db.Track {
	return &db.Track{
		SpotifyID:   spotifyID,
		ISRC:        tags.ISRC,
		Name:        tags.Title,
		ArtistName:  tags.Artist,
		TrackNumber: tags.TrackNumber,
		DiscNumber:  tags.DiscNumber,
		Genre:       tags.Genre,
		ReleaseDate: tags.ReleaseDate,
		AlbumName:   tags.Album,
		AlbumArtist: tags.AlbumArtist,
		Copyright:   tags.Copyright,
	}
}

// catalogTrackFromFile reads path's tags and builds a catalog Track from
// them — the convenience path for callers (jobToCatalogTrack,
// recordCatalogDedupSkip) that have a file path but not an already-read
// FullTrackTags. Returns a bare stub (SpotifyID only) if path is empty;
// best-effort (empty/zero fields) if the file is unreadable.
//
// Hot loops that read every file in a library (scanRootForRebuild) must
// NOT use this — call meta.ReadFullTrackTags once and pass the result to
// CatalogTrackFromTags directly, or this parses the same file twice
// (once for the SpotifyID identification, once here).
func catalogTrackFromFile(spotifyID, path string) *db.Track {
	if path == "" {
		return &db.Track{SpotifyID: spotifyID}
	}
	return CatalogTrackFromTags(spotifyID, meta.ReadFullTrackTags(path))
}

// TrackOverrides carries the subset of catalog Track fields a caller's
// own already-fetched Spotify metadata (Job or JobTrack) can supply,
// applied on top of a file-tag-derived Track. A live download/enqueue's
// Spotify fetch is more trustworthy than whatever ended up embedded in
// the file (formatting/unicode differences, or a file that predates a
// tag-embedding fix) — but only when it actually has a value: a zero
// value here means "Job doesn't know this field," not "this field is
// empty," so it must never blank out a tag-derived value.
type TrackOverrides struct {
	Name, ArtistName, AlbumName, AlbumArtist, ReleaseDate, CoverURL, Copyright string
	TrackNumber, DiscNumber, DurationMs                                        int
}

func ApplyTrackOverrides(t *db.Track, o TrackOverrides) {
	if o.Name != "" {
		t.Name = o.Name
	}
	if o.ArtistName != "" {
		t.ArtistName = o.ArtistName
	}
	if o.AlbumName != "" {
		t.AlbumName = o.AlbumName
	}
	if o.AlbumArtist != "" {
		t.AlbumArtist = o.AlbumArtist
	}
	if o.ReleaseDate != "" {
		t.ReleaseDate = o.ReleaseDate
	}
	if o.CoverURL != "" {
		t.CoverURL = o.CoverURL
	}
	if o.Copyright != "" {
		t.Copyright = o.Copyright
	}
	if o.TrackNumber != 0 {
		t.TrackNumber = o.TrackNumber
	}
	if o.DiscNumber != 0 {
		t.DiscNumber = o.DiscNumber
	}
	if o.DurationMs != 0 {
		t.DurationMs = o.DurationMs
	}
}

// jobToCatalogTrack projects a Job into a catalog Track: starts from
// whatever the downloaded file's own tags say (catalogTrackFromFile),
// then lets Job's fresher Spotify-fetched metadata override name/artist/
// album/etc. ISRC/Genre have no Job-side source at all — both are
// computed transiently inside each provider client during embedding and
// never surfaced back to the caller, so the file is the only place
// either value survives. Best-effort and reads nothing when FilePath is
// empty (failed jobs never had a file) — UpsertTrack's ON CONFLICT
// preserves any previously-known isrc/genre rather than clobbering it
// with this empty read on a later failed retry of the same track.
func jobToCatalogTrack(j *Job) *db.Track {
	t := catalogTrackFromFile(j.SpotifyID, j.FilePath)
	ApplyTrackOverrides(t, TrackOverrides{
		Name:        j.TrackName,
		ArtistName:  j.ArtistName,
		AlbumName:   j.AlbumName,
		AlbumArtist: j.AlbumArtist,
		ReleaseDate: j.ReleaseDate,
		CoverURL:    j.CoverURL,
		Copyright:   j.Copyright,
		TrackNumber: j.TrackNumber,
		DiscNumber:  j.DiscNumber,
		DurationMs:  j.DurationMs,
	})
	return t
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
		Provider:    catalogProvider(j.Settings),
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
		if err := db.UpdateLibraryFileQuality(ctx, q, existing.ID, catalogProvider(j.Settings), newQuality, newSize); err != nil {
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
		Provider:     catalogProvider(j.Settings),
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

// catalogProvider returns the provider value to record in library_files.
// job.Settings.Service can be "" on jobs from watchlists whose settings
// predate a default ever being applied — buildDownloadRequest/
// ExecuteDownload resolve that same empty value to "tidal" locally when
// actually picking a provider, but only on their own local copy of the
// request, never writing it back onto the Job. Without this,
// CreateLibraryFile rejects the row outright ("provider required"),
// silently dropping the catalog entry for an otherwise fully successful
// download — the file exists and is tagged, but never gets a durable
// library_files row, undermining exactly the "survives BoltDB cleanup"
// guarantee the catalog exists for.
func catalogProvider(s JobSettings) string {
	if s.Service == "" {
		return "tidal"
	}
	return s.Service
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
// path. Smaller than CatalogWriteTimeout because we want EnqueueBatch
// fast: callers can wait on a download but not on a 200-track batch
// validation.
const catalogReadTimeout = 2 * time.Second

// catalogDedupResult tells EnqueueBatch how to treat a track based on
// the catalog state. Returned by checkCatalogDedup.
type catalogDedupResult struct {
	skip          bool
	libraryFileID string
	filePath      string
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
		slog.Warn("[Catalog] dedup lookup failed", "spotify_id", spotifyID, "err", err)
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

	// Confirm the file at the recorded path is still actually THIS track,
	// not just that something exists there. Filename templates built purely
	// from title+artist (the default, backend/util/filename.go) have no
	// SpotifyID disambiguation, so two different tracks can collide on the
	// same generated path; without this check, a later download of a
	// DIFFERENT track to that same collided path would make checkCatalogDedup
	// wrongly and permanently believe THIS track is already satisfied.
	if onDiskID, err := meta.ReadSpotifyID(existing.FilePath); err == nil && onDiskID != "" && onDiskID != spotifyID {
		slog.Info("[Catalog] dedup: file actually tagged for a different track, treating as stale",
			"spotify_id", spotifyID, "path", existing.FilePath, "tagged_as", onDiskID)
		return catalogDedupResult{}
	}

	requestedRank := db.QualityRank(deriveCatalogQuality(settings))
	if existing.QualityRank < requestedRank {
		return catalogDedupResult{}
	}

	return catalogDedupResult{
		skip:          true,
		libraryFileID: existing.ID,
		filePath:      existing.FilePath,
		reason: fmt.Sprintf("already in library at %s (rank %d) ≥ requested rank %d",
			existing.Quality, existing.QualityRank, requestedRank),
	}
}

// recordCatalogDedupSkip writes a skipped DownloadAttempt for a track
// that was deduped at enqueue time. No Job ever reached the queue, so
// the caller passes the JobTrack and EnqueueBatchRequest directly.
// filePath is the existing library_file's path (from checkCatalogDedup) —
// dedup only fires when a real file was confirmed on disk, so this reads
// the SAME file's tags catalogTrackFromFile would for a fresh download,
// keeping the two paths' tracks rows consistently enriched instead of
// this one staying a bare stub forever.
//
// BatchID is intentionally left empty: these skips are not part of any
// real batch (no worker did any work for them) and counting them in the
// batch totals would inflate sync stats.
func (jm *JobManager) recordCatalogDedupSkip(track JobTrack, req EnqueueBatchRequest, libraryFileID, filePath, reason string) {
	if jm.catalog == nil || track.SpotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), CatalogWriteTimeout)
	defer cancel()

	t := catalogTrackFromFile(track.SpotifyID, filePath)
	ApplyTrackOverrides(t, TrackOverrides{
		Name:        track.TrackName,
		ArtistName:  track.ArtistName,
		AlbumName:   track.AlbumName,
		AlbumArtist: track.AlbumArtist,
		ReleaseDate: track.ReleaseDate,
		CoverURL:    track.CoverURL,
		Copyright:   track.Copyright,
		TrackNumber: track.TrackNumber,
		DiscNumber:  track.DiscNumber,
		DurationMs:  track.DurationMs,
	})

	if err := db.UpsertTrack(ctx, jm.catalog, t); err != nil {
		slog.Warn("[Catalog] UpsertTrack failed for dedup-skip", "spotify_id", track.SpotifyID, "err", err)
		return
	}

	attempt := &db.DownloadAttempt{
		SpotifyID:     track.SpotifyID,
		LibraryFileID: libraryFileID,
		UserID:        req.UserID,
		WatchlistID:   req.WatchlistID,
		Provider:      catalogProvider(req.Settings),
		Quality:       deriveCatalogQuality(req.Settings),
		Status:        db.AttemptStatusSkipped,
		Error:         reason,
	}
	if err := db.CreateDownloadAttempt(ctx, jm.catalog, attempt); err != nil {
		slog.Warn("[Catalog] CreateDownloadAttempt(dedup-skip) failed", "spotify_id", track.SpotifyID, "err", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Library status verification
// ─────────────────────────────────────────────────────────────────────────────

// LibraryVerifyResult is what one verification pass saw.
type LibraryVerifyResult struct {
	Checked       int
	WentMissing   int
	CameBack      int
	Unchanged     int
	Failed        int
	TimedOut      bool
	MissingSample []string
}

// Changed reports whether the pass found anything worth saying out loud.
func (r LibraryVerifyResult) Changed() bool { return r.WentMissing > 0 || r.CameBack > 0 }

// VerifyLibraryStatuses walks every non-deleted library_files row and reconciles
// its status with what is actually on disk. With apply=false it only counts.
//
// This is the writer library_files.status never had. The column has carried a
// five-state lifecycle since it was introduced, and nothing outside tests ever
// wrote StatusMissing — so every row claimed "present" whether or not the file
// was still there. Consumers that needed a path met an index they could not
// trust, and each grew its own fallback: BoltDB job paths, a full filesystem tag
// scan. Those fallbacks are the drift this repairs at the source. See
// docs/watchlist-consistency-plan.md.
//
// Errors from stat leave the row untouched and count as Failed. That is the
// conservative direction on purpose: an unreadable mount must not be recorded as
// a library that lost every file, which is exactly the state this function
// would otherwise write in one pass.
//
// sampleLimit caps MissingSample. A library that lost thousands of files should
// not produce a response of thousands of paths.
func VerifyLibraryStatuses(ctx context.Context, catalog db.Querier, apply bool, sampleLimit int) (LibraryVerifyResult, error) {
	var result LibraryVerifyResult
	if catalog == nil {
		return result, fmt.Errorf("catalog database is not available")
	}

	files, err := db.ListCheckableLibraryFiles(ctx, catalog)
	if err != nil {
		return result, err
	}

	for _, f := range files {
		if ctx.Err() != nil {
			result.TimedOut = true
			break
		}
		result.Checked++

		onDisk, statErr := statLibraryFile(f.FilePath)
		if statErr != nil {
			result.Failed++
			continue
		}

		switch {
		case !onDisk && f.Status != db.StatusMissing:
			result.WentMissing++
			if len(result.MissingSample) < sampleLimit {
				result.MissingSample = append(result.MissingSample, f.FilePath)
			}
			if apply {
				if err := db.UpdateLibraryFileStatus(ctx, catalog, f.ID, db.StatusMissing); err != nil {
					slog.Warn("[Catalog] verify: could not mark missing", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		case onDisk && f.Status == db.StatusMissing:
			result.CameBack++
			if apply {
				if err := db.UpdateLibraryFileStatus(ctx, catalog, f.ID, db.StatusPresent); err != nil {
					slog.Warn("[Catalog] verify: could not mark present", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		default:
			result.Unchanged++
		}
	}
	return result, nil
}

// statLibraryFile reports whether path is an existing file, keeping "not there"
// (nil error, false) distinct from "could not tell" (non-nil error). The caller
// acts on that distinction: absent means the row is wrong, unreadable means the
// row is left alone.
func statLibraryFile(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
