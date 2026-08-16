package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
)

// TestCatalogFileSizesForWatchlistOnlyCountsPresentFiles is the regression
// test for the "15 MB for a 2500-track playlist" bug: GetWatchlistStats
// used to sum size only from surviving BoltDB job rows, which
// CleanupOldJobs prunes every 24h, so a track downloaded long ago (job
// gone, file still on disk and in the catalog) contributed nothing to
// total_size_mb. catalogFileSizesForWatchlist is the fix's data source —
// it must return every catalog-present track's real file_size, and must
// exclude tracks the catalog has marked missing/deleted even if a row
// still exists for them.
func TestCatalogFileSizesForWatchlistOnlyCountsPresentFiles(t *testing.T) {
	ctx := context.Background()
	catalog, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer catalog.Close()

	seed := []struct {
		spotifyID string
		size      int64
		status    string
	}{
		{"track-present-1", 30 * 1024 * 1024, db.StatusPresent},
		{"track-present-2", 45 * 1024 * 1024, db.StatusPresent},
		{"track-missing", 20 * 1024 * 1024, db.StatusMissing},
		{"track-not-in-playlist", 99 * 1024 * 1024, db.StatusPresent},
	}
	for _, s := range seed {
		if err := db.UpsertTrack(ctx, catalog, &db.Track{SpotifyID: s.spotifyID, Name: s.spotifyID}); err != nil {
			t.Fatalf("UpsertTrack(%s): %v", s.spotifyID, err)
		}
		lf := &db.LibraryFile{
			SpotifyID: s.spotifyID,
			Provider:  "tidal",
			Quality:   db.QualityLossless,
			Format:    "flac",
			FilePath:  "/music/" + s.spotifyID + ".flac",
			FileSize:  s.size,
			Status:    s.status,
		}
		if err := db.CreateLibraryFile(ctx, catalog, lf); err != nil {
			t.Fatalf("CreateLibraryFile(%s): %v", s.spotifyID, err)
		}
	}

	w := &Watcher{catalog: catalog}
	pl := &WatchedPlaylist{
		ID:   "watch-1",
		Name: "Test Playlist",
		TrackIDs: []string{
			"track-present-1", "track-present-2", "track-missing",
			"track-never-downloaded",
		},
	}

	got := w.catalogFileSizesForWatchlist(pl)

	want := map[string]int64{
		"track-present-1": 30 * 1024 * 1024,
		"track-present-2": 45 * 1024 * 1024,
	}
	if len(got) != len(want) {
		t.Fatalf("catalogFileSizesForWatchlist() = %v, want %v", got, want)
	}
	for id, size := range want {
		if got[id] != size {
			t.Errorf("size for %s = %d, want %d", id, got[id], size)
		}
	}
	if _, ok := got["track-missing"]; ok {
		t.Error("a status=missing row must not be counted")
	}
	if _, ok := got["track-not-in-playlist"]; ok {
		t.Error("a track not in pl.TrackIDs must not be counted")
	}
}

func TestCatalogFileSizesForWatchlistNilCatalog(t *testing.T) {
	w := &Watcher{}
	pl := &WatchedPlaylist{ID: "watch-1", TrackIDs: []string{"a", "b"}}
	got := w.catalogFileSizesForWatchlist(pl)
	if len(got) != 0 {
		t.Errorf("expected empty map with nil catalog, got %v", got)
	}
}

// A deletion SpotiFLAC performs itself must land as "deleted", never "missing".
//
// The two states are not interchangeable: "missing" feeds
// POST /api/v1/library/redownload-missing, so a deliberate deletion decaying
// into it makes the app offer to re-download exactly the files a user has just
// chosen to remove. Before this, neither deletion site told the catalog
// anything and the daily verify loop relabelled all of them "missing".
func TestDeleteTrackFileRecordsDeletedNotMissing(t *testing.T) {
	ctx := context.Background()
	catalog, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer catalog.Close()

	root := t.TempDir()
	path := filepath.Join(root, "Artist", "Album", "song.flac")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := db.UpsertTrack(ctx, catalog, &db.Track{SpotifyID: "sid-1", Name: "Song"}); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	if err := db.CreateLibraryFile(ctx, catalog, &db.LibraryFile{
		SpotifyID: "sid-1",
		Provider:  "qobuz",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  path,
		FileSize:  5,
		Status:    db.StatusPresent,
	}); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	w := &Watcher{catalog: catalog}
	if !w.deleteTrackFile("sid-1", path, root, "watchlist removed") {
		t.Fatal("deleteTrackFile reported the file was not removed")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the audio file is still on disk")
	}

	// GetActiveLibraryFile excludes status='deleted', so a nil result IS the
	// assertion that the row was marked — checked explicitly below as well, so
	// a future change to that filter cannot make this test pass by accident.
	if lf, err := db.GetActiveLibraryFile(ctx, catalog, "sid-1"); err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	} else if lf != nil {
		t.Errorf("row is still active with status %q, want it marked deleted", lf.Status)
	}

	var status string
	if err := catalog.QueryRowContext(ctx,
		`SELECT status FROM library_files WHERE spotify_id = ?`, "sid-1").Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != db.StatusDeleted {
		t.Errorf("status = %q, want %q", status, db.StatusDeleted)
	}
	if status == db.StatusMissing {
		t.Error("a deliberate deletion was recorded as missing; redownload-missing would offer it back")
	}

	// And the verify loop must leave it alone from now on: a deleted row is not
	// checkable, so nothing can flip it back.
	checkable, err := db.ListCheckableLibraryFiles(ctx, catalog)
	if err != nil {
		t.Fatalf("ListCheckableLibraryFiles: %v", err)
	}
	for _, f := range checkable {
		if f.FilePath == path {
			t.Error("the deleted row is still checkable; the verify loop would relabel it")
		}
	}
}

// The empty parents go too, and the catalog is told even when the file was
// already gone from disk... except it is not: a file that is not there was not
// deleted by us, and claiming otherwise would mark a row deleted on the
// strength of a failed os.Remove.
func TestDeleteTrackFileLeavesCatalogAloneWhenNothingWasRemoved(t *testing.T) {
	ctx := context.Background()
	catalog, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer catalog.Close()

	if err := db.UpsertTrack(ctx, catalog, &db.Track{SpotifyID: "sid-2", Name: "Song"}); err != nil {
		t.Fatalf("UpsertTrack: %v", err)
	}
	if err := db.CreateLibraryFile(ctx, catalog, &db.LibraryFile{
		SpotifyID: "sid-2",
		Provider:  "qobuz",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  "/nowhere/song.flac",
		FileSize:  5,
		Status:    db.StatusPresent,
	}); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	w := &Watcher{catalog: catalog}
	if w.deleteTrackFile("sid-2", filepath.Join(t.TempDir(), "absent.flac"), t.TempDir(), "watchlist removed") {
		t.Fatal("deleteTrackFile claimed it removed a file that does not exist")
	}

	lf, err := db.GetActiveLibraryFile(ctx, catalog, "sid-2")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if lf == nil {
		t.Fatal("the row was marked deleted even though nothing was removed")
	}
	if lf.Status != db.StatusPresent {
		t.Errorf("status = %q, want it untouched at %q", lf.Status, db.StatusPresent)
	}
}

// A watcher with no catalog must still delete files.
func TestDeleteTrackFileWithoutCatalog(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "song.flac")
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	w := &Watcher{}
	if !w.deleteTrackFile("sid-3", path, root, "dropped from playlist") {
		t.Fatal("deleteTrackFile reported the file was not removed")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the audio file is still on disk")
	}
}
