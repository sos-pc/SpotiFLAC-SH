package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
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
