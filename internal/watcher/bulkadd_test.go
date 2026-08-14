package watcher

import (
	"strings"
	"testing"
)

// A bulk add is partially successful by nature. Eleven watchlists that worked
// must not be rolled back or reported as a failure because the twelfth had a
// dead URL — and the caller has to be told WHICH one, or the only recovery is
// to compare the list by hand.
func TestAddWatchlistsReportsEachOutcome(t *testing.T) {
	w, _ := newTestWatcher(t, false)

	reqs := []AddWatchlistRequest{
		{SpotifyURL: ""},                         // rejected outright
		{SpotifyURL: "not-a-spotify-url-at-all"}, // fails on metadata
	}
	res := w.AddWatchlists(reqs)

	if len(res.Outcomes) != len(reqs) {
		t.Fatalf("got %d outcomes for %d requests", len(res.Outcomes), len(reqs))
	}
	if res.Failed != 2 {
		t.Errorf("Failed = %d, want 2 (outcomes: %+v)", res.Failed, res.Outcomes)
	}
	for _, o := range res.Outcomes {
		if o.Status != "failed" {
			t.Errorf("%q: status = %q, want failed", o.SpotifyURL, o.Status)
		}
		if o.Error == "" {
			t.Errorf("%q: failed with no reason attached", o.SpotifyURL)
		}
		if o.SpotifyURL == "" && !strings.Contains(o.Error, "URL") && !strings.Contains(o.Error, "url") {
			t.Errorf("empty URL rejected with an unhelpful message: %q", o.Error)
		}
	}
}

// An empty request is not a crash and not a silent success.
func TestAddWatchlistsOnNothing(t *testing.T) {
	w, _ := newTestWatcher(t, false)
	res := w.AddWatchlists(nil)
	if res.Added+res.AlreadyWatched+res.Failed != 0 || len(res.Outcomes) != 0 {
		t.Errorf("got %+v for an empty batch", res)
	}
}
