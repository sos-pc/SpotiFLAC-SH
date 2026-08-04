package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
)

// TestActualCatalogQualityFallsBackWhenUnanalyzable covers the safety-net
// path: when the file can't be probed (missing, or ffprobe/ffmpeg isn't
// available — as in this test environment), actualCatalogQuality must
// return the caller-supplied fallback rather than erroring out or
// defaulting to something misleading. The "prefers real delivered quality
// over the request" behavior itself needs a real analyzable audio fixture
// to exercise (this repo has none, and ffmpeg/ffprobe aren't available in
// this test environment either) — verified by code review instead: the
// bit-depth/sample-rate thresholds mirror db.QualityRank's own tier
// boundaries exactly.
func TestActualCatalogQualityFallsBackWhenUnanalyzable(t *testing.T) {
	nonexistent := filepath.Join(t.TempDir(), "does-not-exist.flac")
	got := actualCatalogQuality(nonexistent, db.QualityHiResLossless)
	if got != db.QualityHiResLossless {
		t.Errorf("actualCatalogQuality on unanalyzable file = %q, want fallback %q", got, db.QualityHiResLossless)
	}
}

// writeTestFlacWithTags builds a minimal but real-enough FLAC file with the
// given ISRC/GENRE vorbis comments and a fake frame-sync header, so
// meta.ReadTrackTags can actually parse it back. See
// backend/meta/track_tags_test.go for why the Frames bytes are needed
// (go-flac's readFLACStream indexes unconditionally into frame data).
func writeTestFlacWithTags(t *testing.T, path, isrc, genre string) {
	t.Helper()
	cmt := flacvorbis.New()
	if isrc != "" {
		if err := cmt.Add("ISRC", isrc); err != nil {
			t.Fatalf("add ISRC comment: %v", err)
		}
	}
	if genre != "" {
		if err := cmt.Add("GENRE", genre); err != nil {
			t.Fatalf("add GENRE comment: %v", err)
		}
	}
	block := cmt.Marshal()
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}
	if err := os.WriteFile(path, f.Marshal(), 0644); err != nil {
		t.Fatalf("write test FLAC: %v", err)
	}
}

// TestJobToCatalogTrackReadsTagsFromFile is the regression test for the
// catalog previously never learning a track's ISRC/genre: both are
// computed transiently inside each provider client during embedding and
// never surfaced back onto Job, so jobToCatalogTrack reads them back from
// the file the job actually produced.
func TestJobToCatalogTrackReadsTagsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeTestFlacWithTags(t, path, "USRC17607839", "Synthwave")

	job := &Job{
		SpotifyID:   "spotify:track:abc",
		TrackName:   "Some Track",
		ArtistName:  "Some Artist",
		FilePath:    path,
		ReleaseDate: "2024-03-15",
		AlbumName:   "Some Album",
		AlbumArtist: "Some Album Artist",
		CoverURL:    "https://example.com/cover.jpg",
		Copyright:   "2024 Some Label",
	}

	track := jobToCatalogTrack(job)
	if track.ISRC != "USRC17607839" {
		t.Errorf("ISRC = %q, want %q", track.ISRC, "USRC17607839")
	}
	if track.Genre != "Synthwave" {
		t.Errorf("Genre = %q, want %q", track.Genre, "Synthwave")
	}
	if track.ReleaseDate != job.ReleaseDate || track.AlbumName != job.AlbumName ||
		track.AlbumArtist != job.AlbumArtist || track.CoverURL != job.CoverURL || track.Copyright != job.Copyright {
		t.Errorf("denormalized album fields did not pass through from Job: got %+v", track)
	}
}

// TestJobToCatalogTrackSkipsFileReadWithoutFilePath covers the failed-job
// case: no file was ever produced, so no read should be attempted (and
// none would succeed) — isrc/genre must simply come back empty rather
// than erroring.
func TestJobToCatalogTrackSkipsFileReadWithoutFilePath(t *testing.T) {
	job := &Job{
		SpotifyID:  "spotify:track:abc",
		TrackName:  "Some Track",
		ArtistName: "Some Artist",
		FilePath:   "",
	}
	track := jobToCatalogTrack(job)
	if track.ISRC != "" || track.Genre != "" {
		t.Errorf("ISRC/Genre = (%q, %q), want (\"\", \"\") for a job with no FilePath", track.ISRC, track.Genre)
	}
}

func openTestCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// TestUpsertActiveLibraryFileRefreshesQualityOnSamePath is the regression
// test for the "quality upgrade loops forever" bug: a re-download that
// lands at the same file_path as the current active row (the naming
// template doesn't encode bitrate, so a 16-bit -> 24-bit upgrade of the
// same track usually does) must update the recorded quality in place,
// not silently keep the original value.
func TestUpsertActiveLibraryFileRefreshesQualityOnSamePath(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)

	// checkCatalogDedup stats the file to reject stale rows, so this needs
	// to be a real file, not just a plausible-looking path.
	filePath := filepath.Join(t.TempDir(), "Track.flac")
	if err := os.WriteFile(filePath, []byte("fake flac bytes"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	baseJob := &Job{
		SpotifyID:  "spotify:track:xyz",
		TrackName:  "Some Track",
		ArtistName: "Some Artist",
		FilePath:   filePath,
		TotalSize:  10, // MB, used as fallback if os.Stat fails
		Settings: JobSettings{
			Service:      "tidal",
			TidalQuality: db.QualityLossless,
		},
	}

	// upsertActiveLibraryFile assumes the track row already exists (FK) —
	// in production recordCatalogDone calls UpsertTrack first.
	if err := db.UpsertTrack(ctx, database, jobToCatalogTrack(baseJob)); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	firstID, err := upsertActiveLibraryFile(ctx, database, baseJob)
	if err != nil {
		t.Fatalf("upsertActiveLibraryFile (initial download): %v", err)
	}

	before, err := db.GetActiveLibraryFile(ctx, database, baseJob.SpotifyID)
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if before.Quality != db.QualityLossless {
		t.Fatalf("initial Quality = %q, want %q", before.Quality, db.QualityLossless)
	}

	// Same track, same resolved file path, higher requested quality —
	// this is exactly what a 16-bit -> 24-bit re-download produces.
	upgradedJob := *baseJob
	upgradedJob.Settings.TidalQuality = db.QualityHiResLossless

	secondID, err := upsertActiveLibraryFile(ctx, database, &upgradedJob)
	if err != nil {
		t.Fatalf("upsertActiveLibraryFile (upgrade download): %v", err)
	}
	if secondID != firstID {
		t.Errorf("expected the SAME library_file row to be reused for a same-path upgrade, got a new id %q (was %q)", secondID, firstID)
	}

	after, err := db.GetActiveLibraryFile(ctx, database, baseJob.SpotifyID)
	if err != nil {
		t.Fatalf("GetActiveLibraryFile (after upgrade): %v", err)
	}
	if after.Quality != db.QualityHiResLossless {
		t.Errorf("Quality after same-path upgrade = %q, want %q — the catalog is still stale, dedup will loop forever", after.Quality, db.QualityHiResLossless)
	}
	if after.QualityRank != db.QualityRank(db.QualityHiResLossless) {
		t.Errorf("QualityRank after upgrade = %d, want %d", after.QualityRank, db.QualityRank(db.QualityHiResLossless))
	}

	// checkCatalogDedup should now recognize the upgrade already happened.
	jm := &JobManager{catalog: database}
	result := jm.checkCatalogDedup(baseJob.SpotifyID, JobSettings{Service: "tidal", TidalQuality: db.QualityHiResLossless})
	if !result.skip {
		t.Error("checkCatalogDedup did not recognize the upgrade as satisfied — the infinite re-download loop is not fixed")
	}
}

// TestCatalogProviderDefaultsEmptyServiceToTidal is the regression test
// for "library_file: provider required" write failures observed in
// production: job.Settings.Service can be "" on jobs from watchlists
// whose settings predate a default ever being applied.
// buildDownloadRequest/ExecuteDownload resolve that same empty value to
// "tidal" on their own local copy when actually picking a provider, but
// never write it back onto the Job — so a fully successful download
// (file on disk, correctly tagged) was silently getting no library_files
// row at all, because CreateLibraryFile rejects an empty provider
// outright.
func TestCatalogProviderDefaultsEmptyServiceToTidal(t *testing.T) {
	if got := catalogProvider(JobSettings{Service: ""}); got != "tidal" {
		t.Errorf("catalogProvider(empty Service) = %q, want %q", got, "tidal")
	}
	if got := catalogProvider(JobSettings{Service: "qobuz"}); got != "qobuz" {
		t.Errorf("catalogProvider(qobuz) = %q, want %q (must not override an explicit service)", got, "qobuz")
	}
}

// TestUpsertActiveLibraryFileSucceedsWithEmptyService is the end-to-end
// version of the same regression: a Job with Settings.Service == "" must
// still produce a valid library_files row instead of erroring out of
// upsertActiveLibraryFile entirely.
func TestUpsertActiveLibraryFileSucceedsWithEmptyService(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)

	filePath := filepath.Join(t.TempDir(), "Legacy Track.flac")
	if err := os.WriteFile(filePath, []byte("fake flac bytes"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	job := &Job{
		SpotifyID:  "spotify:track:legacy",
		TrackName:  "Legacy Track",
		ArtistName: "Legacy Artist",
		FilePath:   filePath,
		TotalSize:  10,
		Settings:   JobSettings{}, // Service left empty, as on old watchlist settings
	}
	if err := db.UpsertTrack(ctx, database, jobToCatalogTrack(job)); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}

	id, err := upsertActiveLibraryFile(ctx, database, job)
	if err != nil {
		t.Fatalf("upsertActiveLibraryFile with empty Settings.Service: %v (this must not fail — the download itself succeeded)", err)
	}

	lf, err := db.GetActiveLibraryFile(ctx, database, job.SpotifyID)
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if lf == nil || lf.ID != id {
		t.Fatal("expected the created row to be the active library_file")
	}
	if lf.Provider == "" {
		t.Error("Provider was left empty on the written row")
	}
}

// TestRecordCatalogDedupSkipReadsTagsFromExistingFile is the regression
// test for the third scattered db.Track construction site found while
// auditing this code: recordCatalogDedupSkip fires when the catalog
// already has a confirmed-on-disk file for a track (dedup only skips
// after checkCatalogDedup os.Stat's the existing path), yet it used to
// build its own bare db.Track from JobTrack fields only — never reading
// the file it already knows is there, so isrc/genre (which have no
// JobTrack-side source at all) never made it in through this path.
func TestRecordCatalogDedupSkipReadsTagsFromExistingFile(t *testing.T) {
	jm := newTestJobManager(t, true)
	ctx := context.Background()

	path := filepath.Join(t.TempDir(), "existing.flac")
	writeTestFlacWithTags(t, path, "USRC17607839", "Synthwave")

	track := JobTrack{
		SpotifyID:  "spotify:track:dedup",
		TrackName:  "Existing Track",
		ArtistName: "Existing Artist",
	}
	// Matches the real call pattern: checkCatalogDedup only skips (and
	// recordCatalogDedupSkip only fires) once a real library_files row is
	// confirmed on disk, so the DownloadAttempt it writes can legitimately
	// link to it via FK.
	if err := db.UpsertTrack(ctx, jm.catalog, &db.Track{SpotifyID: track.SpotifyID}); err != nil {
		t.Fatalf("UpsertTrack (seed): %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: track.SpotifyID,
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  path,
	}
	if err := db.CreateLibraryFile(ctx, jm.catalog, lf); err != nil {
		t.Fatalf("CreateLibraryFile (seed): %v", err)
	}

	jm.recordCatalogDedupSkip(track, EnqueueBatchRequest{}, lf.ID, path, "already present")

	got, err := db.GetTrack(ctx, jm.catalog, track.SpotifyID)
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got == nil {
		t.Fatal("GetTrack returned nil")
	}
	if got.Name != track.TrackName || got.ArtistName != track.ArtistName {
		t.Errorf("JobTrack fields not applied: Name=%q ArtistName=%q, want %q/%q", got.Name, got.ArtistName, track.TrackName, track.ArtistName)
	}
	if got.ISRC != "USRC17607839" {
		t.Errorf("ISRC = %q, want %q (must be read from the existing file — JobTrack has no ISRC field)", got.ISRC, "USRC17607839")
	}
	if got.Genre != "Synthwave" {
		t.Errorf("Genre = %q, want %q (must be read from the existing file — JobTrack has no Genre field)", got.Genre, "Synthwave")
	}
}
