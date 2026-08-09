package watcher

import "testing"

// TestSameSpotifySource pins the equivalences the duplicate check depends on.
// The first two cases are the real one: production stores URLs with `?si=…`,
// so the same playlist pasted twice differs as a string while being the same
// entity. String comparison would have called them different and let the
// duplicate through — which is how one deployment ended up with two watchlists
// for one playlist.
func TestSameSpotifySource(t *testing.T) {
	const id = "37i9dQZEVXbmJp8E9E6CHc"
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{
			"same playlist, one carrying the share parameter",
			"https://open.spotify.com/playlist/" + id + "?si=4ad48b2d653240c4",
			"https://open.spotify.com/playlist/" + id,
			true,
		},
		{"URL and URI form", "https://open.spotify.com/playlist/" + id, "spotify:playlist:" + id, true},
		{"localised and plain", "https://open.spotify.com/intl-fr/playlist/" + id, "https://open.spotify.com/playlist/" + id, true},
		{"different playlists", "https://open.spotify.com/playlist/" + id, "https://open.spotify.com/playlist/aaaaaaaaaaaaaaaaaaaaaa", false},
		{
			"same id, different entity kind — an album is not the playlist of the same name",
			"https://open.spotify.com/playlist/" + id,
			"https://open.spotify.com/album/" + id,
			false,
		},
		{"unparseable reference never matches, even itself", "not a spotify link", "not a spotify link", false},
		{"empty never matches", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameSpotifySource(tt.a, tt.b); got != tt.want {
				t.Errorf("sameSpotifySource(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}
