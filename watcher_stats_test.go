package main

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	bolt "go.etcd.io/bbolt"
)

// newTestJobManager creates a JobManager backed by a temp BoltDB and,
// when withCatalog is true, a temp SQLite catalog. Mirrors
// newTestAuthManager (api_keys_test.go)'s pattern.
func newTestJobManager(t *testing.T, withCatalog bool) *JobManager {
	t.Helper()
	f, err := os.CreateTemp("", "spotiflac-test-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	boltDB, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { boltDB.Close() })

	var catalog *sql.DB
	if withCatalog {
		catalogHandle, err := db.Open(t.TempDir())
		if err != nil {
			t.Fatalf("db.Open: %v", err)
		}
		t.Cleanup(func() { catalogHandle.Close() })
		catalog = catalogHandle
	}

	jm, err := NewJobManager(t.TempDir(), boltDB, catalog)
	if err != nil {
		t.Fatalf("NewJobManager: %v", err)
	}
	t.Cleanup(jm.Close)
	return jm
}

// TestGetWatchlistStatsUsesCatalogForSizeAndStatus is the end-to-end
// regression test for the "15 MB for a 2500-track playlist" bug:
// total_size_mb used to be summed only from surviving BoltDB job rows, so
// a track whose job was pruned by CleanupOldJobs (or predates job
// tracking) contributed nothing to the size even though its file is on
// disk and in the catalog. It also covers the corrected "no catalog row,
// no job" case: with the catalog enabled this must now be Pending (it
// genuinely hasn't been downloaded), not silently folded into Skipped.
func TestGetWatchlistStatsUsesCatalogForSizeAndStatus(t *testing.T) {
	jm := newTestJobManager(t, true)
	w := &Watcher{jm: jm}

	pl := &WatchedPlaylist{
		ID:   "watch-stats",
		Name: "Stats Playlist",
		TrackIDs: []string{
			"catalog-only",  // job pruned, file still present per the catalog
			"job-done",      // job still present, not (yet) in the catalog
			"job-failed",    // job failed
			"never-touched", // no job, no catalog row
		},
	}
	if err := w.saveWatchlist(pl); err != nil {
		t.Fatalf("saveWatchlist: %v", err)
	}

	// Seed the catalog: a track whose BoltDB job is long gone, 40 MB file.
	if err := db.UpsertTrack(context.Background(), jm.catalog, &db.Track{SpotifyID: "catalog-only", Name: "Catalog Only"}); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	if err := db.CreateLibraryFile(context.Background(), jm.catalog, &db.LibraryFile{
		SpotifyID: "catalog-only",
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  "/music/catalog-only.flac",
		FileSize:  40 * 1024 * 1024,
		Status:    db.StatusPresent,
	}); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	// A job still present in BoltDB for a track not yet in the catalog.
	if err := jm.saveJob(&Job{
		ID:          "job-1",
		SpotifyID:   "job-done",
		WatchlistID: pl.ID,
		Status:      StatusDone,
		TotalSize:   12.5, // MB
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("saveJob(job-done): %v", err)
	}
	if err := jm.saveJob(&Job{
		ID:          "job-2",
		SpotifyID:   "job-failed",
		WatchlistID: pl.ID,
		Status:      StatusFailed,
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("saveJob(job-failed): %v", err)
	}

	stats, err := w.GetWatchlistStats(pl.ID)
	if err != nil {
		t.Fatalf("GetWatchlistStats: %v", err)
	}

	if stats.TotalTracks != 4 {
		t.Errorf("TotalTracks = %d, want 4", stats.TotalTracks)
	}
	if stats.Downloaded != 2 {
		t.Errorf("Downloaded = %d, want 2 (catalog-only + job-done)", stats.Downloaded)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
	if stats.Pending != 1 {
		t.Errorf("Pending = %d, want 1 (never-touched, catalog enabled)", stats.Pending)
	}
	if stats.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", stats.Skipped)
	}

	wantSizeMB := 40.0 + 12.5
	if diff := stats.TotalSizeMB - wantSizeMB; diff > 0.01 || diff < -0.01 {
		t.Errorf("TotalSizeMB = %f, want %f (this is the regression: must include the catalog-only track's real size, not just surviving job sizes)", stats.TotalSizeMB, wantSizeMB)
	}
}

// TestGetWatchlistStatsFallsBackToSkippedWithoutCatalog preserves the old
// behaviour when the catalog is disabled: a track with no job at all is
// assumed already downloaded (pre-tracking / job pruned) rather than
// reported as pending, since without a catalog there's no way to
// distinguish the two cases.
func TestGetWatchlistStatsFallsBackToSkippedWithoutCatalog(t *testing.T) {
	jm := newTestJobManager(t, false)
	w := &Watcher{jm: jm}

	pl := &WatchedPlaylist{
		ID:       "watch-nocat",
		Name:     "No Catalog Playlist",
		TrackIDs: []string{"orphan-track"},
	}
	if err := w.saveWatchlist(pl); err != nil {
		t.Fatalf("saveWatchlist: %v", err)
	}

	stats, err := w.GetWatchlistStats(pl.ID)
	if err != nil {
		t.Fatalf("GetWatchlistStats: %v", err)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (jobless track, no catalog to consult)", stats.Skipped)
	}
	if stats.Pending != 0 {
		t.Errorf("Pending = %d, want 0", stats.Pending)
	}
}

// TestOnBatchCompleteSurvivesSyncLogEviction is the regression test for a
// silent-data-loss bug in the "Recent syncs" panel: OnBatchComplete looks
// up the SyncLog entry matching a batch's ID to fill in its
// downloaded/skipped/failed counts, but that entry can be evicted by the
// 20-entry cap before the batch finishes — jobWorkers=1 serializes every
// download across every watchlist through one shared queue, so on a busy
// instance a large batch can easily outlive 20 of this watchlist's own
// sync cycles. Previously, a missed match meant the counts were silently
// dropped entirely (no save, no fallback). Now a standalone entry must be
// appended instead.
func TestOnBatchCompleteSurvivesSyncLogEviction(t *testing.T) {
	jm := newTestJobManager(t, false)
	w := &Watcher{jm: jm}

	pl := &WatchedPlaylist{ID: "watch-evict", Name: "Busy Playlist"}
	// Fill SyncLogs to the cap with entries that do NOT carry the batch ID
	// OnBatchComplete will report — simulating 20 sync cycles that ran
	// while the original batch was still draining through the single
	// worker.
	for i := 0; i < 20; i++ {
		pl.SyncLogs = append(pl.SyncLogs, SyncLog{
			Time:    time.Now(),
			BatchID: "some-other-batch",
		})
	}
	if err := w.saveWatchlist(pl); err != nil {
		t.Fatalf("saveWatchlist: %v", err)
	}

	w.OnBatchComplete(pl.ID, "the-evicted-batch", 5, 2, 1)

	got, err := w.getWatchlistByID(pl.ID)
	if err != nil {
		t.Fatalf("getWatchlistByID: %v", err)
	}
	if len(got.SyncLogs) != 20 {
		t.Fatalf("SyncLogs length = %d, want 20 (cap preserved)", len(got.SyncLogs))
	}
	last := got.SyncLogs[len(got.SyncLogs)-1]
	if last.BatchID != "the-evicted-batch" || last.Downloaded != 5 || last.Skipped != 2 || last.Failed != 1 {
		t.Errorf("last SyncLog entry = %+v, want a standalone entry with the batch's counts (they must not be silently dropped)", last)
	}
}
