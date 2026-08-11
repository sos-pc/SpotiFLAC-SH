package watcher

import "testing"

// nameIn calls decideM3U8Name with every watchlist in one root and a fixed label
// per user, which is the arrangement the escalation is about.
func nameIn(pl *WatchedPlaylist, all []WatchedPlaylist, labels map[string]string) string {
	return decideM3U8Name(pl.EffectiveName(), pl.UserID, pl.ID,
		all,
		"/music",
		func(*WatchedPlaylist) string { return "/music" },
		func(uid string) string { return labels[uid] },
	)
}

// TestM3U8NameEscalation walks the three rungs. The point of the ladder is that
// each one only appears when the one below cannot tell two files apart, so the
// common case carries nothing at all.
func TestM3U8NameEscalation(t *testing.T) {
	labels := map[string]string{"u1": "methammer", "u2": "paul"}

	alone := WatchedPlaylist{ID: "w1", Name: "Release Radar", UserID: "u1"}
	otherUser := WatchedPlaylist{ID: "w2", Name: "Release Radar", UserID: "u2"}
	sameUser := WatchedPlaylist{ID: "w3", Name: "Release Radar", UserID: "u1"}
	unrelated := WatchedPlaylist{ID: "w4", Name: "Jazz", UserID: "u2"}

	t.Run("nothing else claims the name", func(t *testing.T) {
		got := nameIn(&alone, []WatchedPlaylist{alone, unrelated}, labels)
		if got != "Release Radar" {
			t.Errorf("got %q, want a bare name", got)
		}
	})

	t.Run("another account claims it: labelled, both sides", func(t *testing.T) {
		all := []WatchedPlaylist{alone, otherUser}
		if got := nameIn(&alone, all, labels); got != "Release Radar (methammer)" {
			t.Errorf("got %q, want the account label", got)
		}
		if got := nameIn(&otherUser, all, labels); got != "Release Radar (paul)" {
			t.Errorf("other side got %q, want its own label", got)
		}
	})

	t.Run("same account claims it twice: no readable distinction left", func(t *testing.T) {
		all := []WatchedPlaylist{alone, sameUser}
		a := nameIn(&alone, all, labels)
		b := nameIn(&sameUser, all, labels)
		if a == b {
			t.Fatalf("both watchlists produced %q — one would overwrite the other", a)
		}
		if a == "Release Radar" || b == "Release Radar" {
			t.Errorf("expected both to be disambiguated, got %q and %q", a, b)
		}
	})

	t.Run("no label available falls back rather than colliding", func(t *testing.T) {
		all := []WatchedPlaylist{alone, otherUser}
		a := nameIn(&alone, all, map[string]string{})
		b := nameIn(&otherUser, all, map[string]string{})
		if a == b {
			t.Fatalf("both produced %q with no labels to tell them apart", a)
		}
	})
}

// TestM3U8NameCustomWins: a name the user typed is what the file is called, and
// it is what collisions are judged on.
func TestM3U8NameCustomWins(t *testing.T) {
	labels := map[string]string{"u1": "methammer", "u2": "paul"}
	mine := WatchedPlaylist{ID: "w1", Name: "Release Radar", CustomName: "Ma selection", UserID: "u1"}
	theirs := WatchedPlaylist{ID: "w2", Name: "Release Radar", UserID: "u2"}

	// Renaming mine away from "Release Radar" leaves theirs unambiguous.
	all := []WatchedPlaylist{mine, theirs}
	if got := nameIn(&mine, all, labels); got != "Ma selection" {
		t.Errorf("custom name: got %q", got)
	}
	if got := nameIn(&theirs, all, labels); got != "Release Radar" {
		t.Errorf("the other side should no longer need a label, got %q", got)
	}
}

// TestM3U8NameIgnoresNonColliders: albums write no file, and a watchlist in
// another download root is not in this one's way.
func TestM3U8NameIgnoresNonColliders(t *testing.T) {
	labels := map[string]string{"u1": "methammer", "u2": "paul"}
	mine := WatchedPlaylist{ID: "w1", Name: "Release Radar", UserID: "u1"}
	album := WatchedPlaylist{ID: "w2", Name: "Release Radar", UserID: "u2",
		SpotifyURL: "https://open.spotify.com/album/1DFixLWuPkv3KT3TnV35m3"}
	elsewhere := WatchedPlaylist{ID: "w3", Name: "Release Radar", UserID: "u2"}

	if got := nameIn(&mine, []WatchedPlaylist{mine, album}, labels); got != "Release Radar" {
		t.Errorf("an album watchlist writes no file and must not force a suffix, got %q", got)
	}

	got := decideM3U8Name(mine.EffectiveName(), mine.UserID, mine.ID,
		[]WatchedPlaylist{mine, elsewhere}, "/music",
		func(p *WatchedPlaylist) string {
			if p.ID == "w3" {
				return "/other-music"
			}
			return "/music"
		},
		func(uid string) string { return labels[uid] })
	if got != "Release Radar" {
		t.Errorf("a watchlist in another root cannot collide, got %q", got)
	}
}
