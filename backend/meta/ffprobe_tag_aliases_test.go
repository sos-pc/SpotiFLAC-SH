package meta

import "testing"

// ffprobe does not normalise container tag names, and the two tag readers that
// existed until 2026-07-19 disagreed about which spellings to accept. The File
// Manager's reader took both spellings; this one took a single one. Routing the
// File Manager through here without these aliases would have silently dropped
// disc numbers, album artists, years and ISRCs on whichever M4A files happened
// to use the other spelling.
func TestFirstTagAcceptsBothSpellings(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		keys []string
		want string
	}{
		{"disk", map[string]string{"disk": "2/5"}, []string{"disk", "disc"}, "2/5"},
		{"disc", map[string]string{"disc": "2/5"}, []string{"disk", "disc"}, "2/5"},
		{"album_artist", map[string]string{"album_artist": "X"}, []string{"album_artist", "albumartist"}, "X"},
		{"albumartist", map[string]string{"albumartist": "X"}, []string{"album_artist", "albumartist"}, "X"},
		{"date", map[string]string{"date": "2020"}, []string{"date", "year"}, "2020"},
		{"year", map[string]string{"year": "2020"}, []string{"date", "year"}, "2020"},
		{"isrc", map[string]string{"isrc": "GB123"}, []string{"isrc", "tsrc"}, "GB123"},
		{"tsrc", map[string]string{"tsrc": "GB123"}, []string{"isrc", "tsrc"}, "GB123"},

		// Order matters: the canonical spelling wins when both are present.
		{"les deux présents", map[string]string{"disk": "1", "disc": "9"}, []string{"disk", "disc"}, "1"},
		// A key present but blank must not shadow the alternative spelling.
		{"premier vide", map[string]string{"disk": "  ", "disc": "7"}, []string{"disk", "disc"}, "7"},
		{"aucun", map[string]string{}, []string{"disk", "disc"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstTag(tc.tags, tc.keys...); got != tc.want {
				t.Errorf("firstTag(%v, %v) = %q, want %q", tc.tags, tc.keys, got, tc.want)
			}
		})
	}
}
