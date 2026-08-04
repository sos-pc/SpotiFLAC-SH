package main

import (
	"context"
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

	w := &Watcher{jm: &JobManager{catalog: catalog}}
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
	w := &Watcher{jm: &JobManager{}}
	pl := &WatchedPlaylist{ID: "watch-1", TrackIDs: []string{"a", "b"}}
	got := w.catalogFileSizesForWatchlist(pl)
	if len(got) != 0 {
		t.Errorf("expected empty map with nil catalog, got %v", got)
	}
}
