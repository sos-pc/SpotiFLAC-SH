package spotify

import "testing"

// TestParseEntityRef pins the shapes a Spotify reference actually arrives in.
// The localised case is the regression: the anchored pattern this replaced
// returned nothing for open.spotify.com/intl-fr/..., so any caller comparing
// IDs would have treated a localised link as unknown.
func TestParseEntityRef(t *testing.T) {
	tests := []struct {
		ref       string
		wantKind  string
		wantID    string
	}{
		{"https://open.spotify.com/playlist/37i9dQZEVXbmJp8E9E6CHc", "playlist", "37i9dQZEVXbmJp8E9E6CHc"},
		{"https://open.spotify.com/playlist/37i9dQZEVXbmJp8E9E6CHc?si=4ad48b2d653240c4", "playlist", "37i9dQZEVXbmJp8E9E6CHc"},
		{"spotify:playlist:37i9dQZEVXbmJp8E9E6CHc", "playlist", "37i9dQZEVXbmJp8E9E6CHc"},
		{"https://open.spotify.com/intl-fr/playlist/37i9dQZEVXbmJp8E9E6CHc", "playlist", "37i9dQZEVXbmJp8E9E6CHc"},
		{"https://open.spotify.com/intl-fr/album/1DFixLWuPkv3KT3TnV35m3?si=x", "album", "1DFixLWuPkv3KT3TnV35m3"},
		{"https://open.spotify.com/artist/0OdUWJ0sBjDrqHygGUXeCF", "artist", "0OdUWJ0sBjDrqHygGUXeCF"},
		{"https://example.com/not-spotify/playlist/abc", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			kind, id := ParseEntityRef(tt.ref)
			if kind != tt.wantKind || id != tt.wantID {
				t.Errorf("ParseEntityRef(%q) = (%q, %q), want (%q, %q)", tt.ref, kind, id, tt.wantKind, tt.wantID)
			}
		})
	}
}
