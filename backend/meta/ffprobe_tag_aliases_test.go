package meta

import "testing"

// ffprobe does not normalise container tag names, and the two tag readers that
// existed until 2026-07-19 disagreed about which spellings to accept. The
// disagreement had two shapes: on album artist, year and ISRC the File Manager's
// reader took both spellings while this one took a single one; on the disc
// number they each took a *different* single key ("disc" there, "disk" here).
// Either way, routing the File Manager through this reader without a union
// would have silently dropped data on M4A files using the other spelling.
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

// The release date is the one field where "first spelling wins" is the wrong
// rule: DATE and YEAR routinely disagree in precision rather than in content,
// and the reader this replaced kept the longer value on purpose.
func TestLongestTagKeepsThePreciseDate(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want string
	}{
		{"date seul", map[string]string{"date": "2020"}, "2020"},
		{"year seul", map[string]string{"year": "2020"}, "2020"},
		{"year plus précis que date", map[string]string{"date": "2020", "year": "2020-03-05"}, "2020-03-05"},
		{"date plus précise que year", map[string]string{"date": "2020-03-05", "year": "2020"}, "2020-03-05"},
		{"identiques", map[string]string{"date": "2020", "year": "2020"}, "2020"},
		{"un seul vide", map[string]string{"date": "   ", "year": "2020"}, "2020"},
		{"aucun", map[string]string{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := longestTag(tc.tags, "date", "year"); got != tc.want {
				t.Errorf("longestTag(%v) = %q, want %q", tc.tags, got, tc.want)
			}
		})
	}
}
