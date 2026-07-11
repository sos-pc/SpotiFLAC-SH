package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	bolt "go.etcd.io/bbolt"
)

// These tests exercise syncCatalogPathOnRename's guard clauses and its
// safe-failure behavior on a file with no readable SPOTIFY_ID tag. The full
// happy path (real tagged audio file -> catalog row updated) needs a real
// FLAC/MP3 fixture to exercise meta.ReadSpotifyID end-to-end, which this
// package doesn't have test fixtures for; that path was verified by code
// review instead (ReadSpotifyID is the same function library-rebuild
// already relies on in api_admin_test.go's ingest tests, just called with a
// literal path instead of via a filesystem walk).

func TestSyncCatalogPathOnRenameNoOpsSafely(t *testing.T) {
	database := openTestCatalogDB(t)
	ctr := &Container{Catalog: database}

	t.Run("nil container", func(t *testing.T) {
		syncCatalogPathOnRename(nil, "/a", "/b") // must not panic
	})
	t.Run("nil catalog", func(t *testing.T) {
		syncCatalogPathOnRename(&Container{}, "/a", "/b") // must not panic
	})
	t.Run("same path", func(t *testing.T) {
		syncCatalogPathOnRename(ctr, "/a", "/a") // no-op, must not touch the DB
	})
	t.Run("empty paths", func(t *testing.T) {
		syncCatalogPathOnRename(ctr, "", "/b")
		syncCatalogPathOnRename(ctr, "/a", "")
	})
}

func TestSyncCatalogPathOnRenameSkipsUntaggedFile(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)
	ctr := &Container{Catalog: database}

	oldPath := filepath.Join(t.TempDir(), "old.flac")
	newPath := filepath.Join(t.TempDir(), "new.flac")
	if err := os.WriteFile(newPath, []byte("not a real audio file"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := db.UpsertTrackStub(ctx, database, "spotify:track:rename-test"); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: "spotify:track:rename-test",
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  oldPath,
	}
	if err := db.CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	// newPath isn't a parseable audio file, so ReadSpotifyID fails and this
	// must be a safe no-op — the catalog row must stay exactly as it was.
	syncCatalogPathOnRename(ctr, oldPath, newPath)

	got, err := db.GetActiveLibraryFile(ctx, database, "spotify:track:rename-test")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if got.FilePath != oldPath {
		t.Errorf("FilePath = %q, want unchanged %q (rename sync should have no-opped on an unparseable file)", got.FilePath, oldPath)
	}
}

// TestSyncCatalogPathOnRenamePropagatesToJobsAndHistory is the end-to-end
// regression test for N3/N9: a rename must keep all three independent
// path-holding stores in sync, not just the SQLite catalog. Before this
// fix, only the catalog was updated — BoltDB Job.FilePath and
// HistoryItem.Path both kept the stale oldPath, which meant
// recoverMissingFiles would think the (still-present, just renamed) file
// had vanished and redundantly re-download it, and the playlist-removal
// path's os.Remove(job.FilePath) would silently fail on the stale path,
// leaking the actual file on disk forever.
func TestSyncCatalogPathOnRenamePropagatesToJobsAndHistory(t *testing.T) {
	ctx := context.Background()
	const spotifyID = "spotify:track:rename-propagation-test"

	database := openTestCatalogDB(t)

	oldPath := filepath.Join(t.TempDir(), "old.flac")
	newPath := filepath.Join(t.TempDir(), "new.flac")
	writeTestFlacWithTags(t, newPath, "", "")
	if _, err := meta.WriteSpotifyIDTag(newPath, spotifyID); err != nil {
		t.Fatalf("WriteSpotifyIDTag: %v", err)
	}

	if err := db.UpsertTrackStub(ctx, database, spotifyID); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	if err := db.CreateLibraryFile(ctx, database, &db.LibraryFile{
		SpotifyID: spotifyID,
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  oldPath,
	}); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	jm := newTestJobManager(t, false)
	job := &Job{ID: "job-rename-test", SpotifyID: spotifyID, FilePath: oldPath, Status: StatusDone}
	if err := jm.saveJob(job); err != nil {
		t.Fatalf("saveJob: %v", err)
	}

	historyBoltDB, err := bolt.Open(filepath.Join(t.TempDir(), "history-test.db"), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open (history): %v", err)
	}
	t.Cleanup(func() { historyBoltDB.Close() })
	if err := backend.InitHistoryDBShared(historyBoltDB); err != nil {
		t.Fatalf("InitHistoryDBShared: %v", err)
	}
	if err := backend.AddHistoryItem(backend.HistoryItem{SpotifyID: spotifyID, Path: oldPath}); err != nil {
		t.Fatalf("AddHistoryItem: %v", err)
	}

	ctr := &Container{Catalog: database, Jobs: jm}
	syncCatalogPathOnRename(ctr, oldPath, newPath)

	// 1. Catalog updated.
	lf, err := db.GetActiveLibraryFile(ctx, database, spotifyID)
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if lf.FilePath != newPath {
		t.Errorf("catalog FilePath = %q, want %q", lf.FilePath, newPath)
	}

	// 2. BoltDB job updated (N3).
	gotJob, err := jm.GetJob("job-rename-test")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if gotJob.FilePath != newPath {
		t.Errorf("job.FilePath = %q, want %q", gotJob.FilePath, newPath)
	}

	// 3. Download history updated (N9).
	items, err := backend.GetHistoryItems("")
	if err != nil {
		t.Fatalf("GetHistoryItems: %v", err)
	}
	if len(items) != 1 || items[0].Path != newPath {
		t.Errorf("history items = %+v, want exactly one item with Path %q", items, newPath)
	}
}
