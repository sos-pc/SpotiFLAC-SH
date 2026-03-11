package main

import "testing"

func TestNormalizeSpotifyURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"URL normale inchangée",       "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l",                "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l"},
		{"intl-fr supprimé",            "https://open.spotify.com/intl-fr/track/4ozKsccDY52n3IEwbBJx0l",       "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l"},
		{"?si= supprimé",               "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l?si=abc123",     "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l"},
		{"intl-fr + ?si= ensemble",     "https://open.spotify.com/intl-fr/album/4ozKsccDY52n3IEwbBJx0l?si=xyz","https://open.spotify.com/album/4ozKsccDY52n3IEwbBJx0l"},
		{"playlist inchangée",          "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",            "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M"},
		{"URL vide retournée telle quelle", "",                                                                  ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeSpotifyURL(tt.input)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
