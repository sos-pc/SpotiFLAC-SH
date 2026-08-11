package watcher

import (
	"testing"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
)

// TestHistorySaysWhichRowsStillDescribeThePresent covers the reason the card and
// the History tab looked like they contradicted each other.
//
// The card partitions the tracks currently in the playlist; History lists
// attempts. A track that failed and then succeeded is one track above and two
// rows below, so "0 failed" over a list containing a failure is two correct
// answers to two questions — with nothing in the data saying so.
func TestHistorySaysWhichRowsStillDescribeThePresent(t *testing.T) {
	w, jm := newTestWatcher(t, false)

	putWatchlist(t, w, WatchedPlaylist{
		ID:       "watch-1",
		Name:     "Jazz",
		TrackIDs: []string{"kept"},
	})

	base := time.Now().Add(-time.Hour)
	seed := []jobs.Job{
		// Same track, failed then succeeded: the older row is superseded.
		{ID: "j1", WatchlistID: "watch-1", SpotifyID: "kept", TrackName: "Kept",
			Status: jobs.StatusFailed, Error: "boom", UpdatedAt: base},
		{ID: "j2", WatchlistID: "watch-1", SpotifyID: "kept", TrackName: "Kept",
			Status: jobs.StatusDone, UpdatedAt: base.Add(time.Minute)},
		// A track that has since left the playlist: its attempt stays in the
		// log, but nothing on the card counts it.
		{ID: "j3", WatchlistID: "watch-1", SpotifyID: "dropped", TrackName: "Dropped",
			Status: jobs.StatusFailed, Error: "boom", UpdatedAt: base},
		// Another watchlist's job must not appear at all.
		{ID: "j4", WatchlistID: "watch-2", SpotifyID: "elsewhere", TrackName: "Elsewhere",
			Status: jobs.StatusDone, UpdatedAt: base},
	}
	for i := range seed {
		if err := jm.SaveJob(&seed[i]); err != nil {
			t.Fatalf("SaveJob(%s): %v", seed[i].ID, err)
		}
	}

	items, err := w.GetWatchlistHistory("watch-1")
	if err != nil {
		t.Fatalf("GetWatchlistHistory: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d rows, want the 3 belonging to watch-1", len(items))
	}

	byID := map[string]WatchlistHistoryItem{}
	for _, it := range items {
		key := it.SpotifyID + ":" + it.Status
		byID[key] = it
	}

	if row := byID["kept:failed"]; !row.Superseded {
		t.Error("the failed attempt was not marked superseded — it is why the card shows 0 failed")
	}
	if row := byID["kept:done"]; row.Superseded {
		t.Error("the newest attempt was marked superseded")
	}
	if row := byID["kept:done"]; !row.StillTracked {
		t.Error("a track still in the playlist was marked untracked")
	}
	if row := byID["dropped:failed"]; row.StillTracked {
		t.Error("a track that left the playlist was still marked tracked")
	}
	if row := byID["dropped:failed"]; row.Superseded {
		t.Error("the only attempt for a track was marked superseded")
	}
}

// TestHistoryDoesNotClaimUntrackedWhenTheWatchlistCannotBeRead: the flag is
// derived from the watchlist's TrackIDs, so a read failure must not silently
// relabel every row "no longer in the playlist" — that would grey out an entire
// history and read as data loss.
func TestHistoryDoesNotClaimUntrackedWhenTheWatchlistCannotBeRead(t *testing.T) {
	w, jm := newTestWatcher(t, false)
	// No watchlist record is stored, so GetWatchlistByID fails.
	if err := jm.SaveJob(&jobs.Job{
		ID: "j1", WatchlistID: "watch-gone", SpotifyID: "x", TrackName: "X",
		Status: jobs.StatusDone, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	items, err := w.GetWatchlistHistory("watch-gone")
	if err != nil {
		t.Fatalf("GetWatchlistHistory: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d rows, want 1", len(items))
	}
	if !items[0].StillTracked {
		t.Error("an unreadable watchlist turned its history into 'no longer tracked'")
	}
}
