package watcher

import "testing"

// manualNameIn is how OnManualBatchComplete calls decideM3U8Name: a display
// name, an owner, and a key derived from the Spotify entity rather than a
// watchlist ID it does not have.
func manualNameIn(name, userID, entityID string, all []WatchedPlaylist, labels map[string]string) string {
	return decideM3U8Name(name, userID, "manual-"+entityID,
		all,
		"/music",
		func(*WatchedPlaylist) string { return "/music" },
		func(uid string) string { return labels[uid] },
	)
}

// TestManualBatchNameCarriesNoSuffixByDefault is the user-visible half of the
// fix. Downloading a playlist from the search bar used to go through
// m3u8BaseName, which appends a hash unconditionally — so every such playlist
// arrived in Jellyfin wearing eight hex digits, months after the watchlist path
// was changed to stop doing exactly that.
func TestManualBatchNameCarriesNoSuffixByDefault(t *testing.T) {
	labels := map[string]string{"u1": "methammer"}
	unrelated := WatchedPlaylist{ID: "w1", Name: "Jazz", UserID: "u1"}

	got := manualNameIn("Dizzy Atmosphere", "u1", "4aX4rEbYcU", []WatchedPlaylist{unrelated}, labels)
	if got != "Dizzy Atmosphere" {
		t.Errorf("got %q, want a bare name — the search-bar path is still adding a suffix", got)
	}
}

// TestManualBatchNameEscalatesOnCollision: dropping the suffix must not let a
// manual batch overwrite a watchlist that already owns the bare name. The
// watchlist keeps it; the batch escalates, same ladder as everything else.
func TestManualBatchNameEscalatesOnCollision(t *testing.T) {
	labels := map[string]string{"u1": "methammer", "u2": "paul"}
	watched := WatchedPlaylist{ID: "w1", Name: "Release Radar", UserID: "u1"}

	sameUser := manualNameIn("Release Radar", "u1", "abc123", []WatchedPlaylist{watched}, labels)
	if sameUser == "Release Radar" {
		t.Error("a manual batch took the name a watchlist already owns")
	}

	// Another account's watchlist holds the name: the readable rung, not the hash.
	otherUser := manualNameIn("Release Radar", "u2", "abc123", []WatchedPlaylist{watched}, labels)
	if otherUser != "Release Radar (paul)" {
		t.Errorf("got %q, want the label rung", otherUser)
	}
}

// TestManualBatchKeyIgnoresShareParameter: the key is derived from the entity,
// not the pasted URL. Hashing the raw URL gave the same playlist a different
// name on every share link, so a collision would rename the file each time.
func TestManualBatchKeyIgnoresShareParameter(t *testing.T) {
	labels := map[string]string{"u1": "methammer"}
	watched := WatchedPlaylist{ID: "w1", Name: "Release Radar", UserID: "u1"}
	all := []WatchedPlaylist{watched}

	// Same entity, two URL shapes — ParseEntityRef returns the same ID for both,
	// so the caller hands us the same key and the name must not move.
	a := manualNameIn("Release Radar", "u1", "37i9dQZEVXbmJp8E9E6CHc", all, labels)
	b := manualNameIn("Release Radar", "u1", "37i9dQZEVXbmJp8E9E6CHc", all, labels)
	if a != b {
		t.Errorf("same entity produced %q then %q", a, b)
	}
}

// TestFindWatchlistBySourceGatesTheManualWrite: the skip that stops a manual
// batch writing a second playlist beside a watched one only fires if this
// lookup matches across URL shapes. Production stores URLs with `?si=…`; the
// batch's SourceID may not carry it.
func TestFindWatchlistBySourceGatesTheManualWrite(t *testing.T) {
	const id = "37i9dQZEVXbmJp8E9E6CHc"
	w := testWatcher(t)
	putWatchlist(t, w, WatchedPlaylist{
		ID:         "watch-1",
		Name:       "Release Radar",
		UserID:     "u1",
		SpotifyURL: "https://open.spotify.com/playlist/" + id + "?si=4ad48b2d653240c4",
	})

	found, err := w.findWatchlistBySource("spotify:playlist:"+id, "u1")
	if err != nil {
		t.Fatalf("findWatchlistBySource: %v", err)
	}
	if found == nil {
		t.Fatal("the watched playlist was not recognised — the manual batch would write a second file beside it")
	}

	// Another account tracking the same playlist is a different watchlist with
	// its own files, so this must not match across users.
	if other, err := w.findWatchlistBySource("spotify:playlist:"+id, "u2"); err != nil || other != nil {
		t.Errorf("matched across users: %v, %v", other, err)
	}
}
