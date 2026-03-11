package backend

import "testing"

func TestParseSpotifyURI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantType string
		wantID   string
		wantErr  bool
	}{
		{"track standard",       "https://open.spotify.com/track/4ozKsccDY52n3IEwbBJx0l",         "track",    "4ozKsccDY52n3IEwbBJx0l", false},
		{"album standard",       "https://open.spotify.com/album/4ozKsccDY52n3IEwbBJx0l",         "album",    "4ozKsccDY52n3IEwbBJx0l", false},
		{"playlist standard",    "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",      "playlist", "37i9dQZF1DXcBWIGoYBM5M", false},
		{"artiste standard",     "https://open.spotify.com/artist/2dihR1jdCJPA8xR1SfzBl6",        "artist",   "2dihR1jdCJPA8xR1SfzBl6", false},
		{"avec intl-fr",         "https://open.spotify.com/intl-fr/track/4ozKsccDY52n3IEwbBJx0l", "track",    "4ozKsccDY52n3IEwbBJx0l", false},
		{"spotify: track",       "spotify:track:4ozKsccDY52n3IEwbBJx0l",                          "track",    "4ozKsccDY52n3IEwbBJx0l", false},
		{"spotify: album",       "spotify:album:4ozKsccDY52n3IEwbBJx0l",                          "album",    "4ozKsccDY52n3IEwbBJx0l", false},
		{"URL vide → erreur",    "",                                                                "", "", true},
		{"non-Spotify → erreur", "https://youtube.com/watch?v=abc",                               "", "", true},
		{"path vide → erreur",   "https://open.spotify.com/",                                     "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSpotifyURI(tt.input)
			if tt.wantErr {
				if err == nil { t.Errorf("parseSpotifyURI(%q) : erreur attendue", tt.input) }
				return
			}
			if err != nil { t.Errorf("parseSpotifyURI(%q) : erreur inattendue: %v", tt.input, err); return }
			if got.Type != tt.wantType { t.Errorf("Type = %q, want %q", got.Type, tt.wantType) }
			if got.ID   != tt.wantID   { t.Errorf("ID = %q, want %q",   got.ID,   tt.wantID)   }
		})
	}
}
