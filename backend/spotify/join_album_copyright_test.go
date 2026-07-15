package spotify

import "testing"

// copyrightAlbum builds a minimal albumUnion-shaped map carrying only a
// copyright block, matching the real GraphQL shape ({"copyright": {"items":
// [{"type": "C"|"P", "text": "..."}]}}) that joinAlbumCopyright reads.
func copyrightAlbum(items ...[2]string) map[string]interface{} {
	list := make([]interface{}, 0, len(items))
	for _, it := range items {
		list = append(list, map[string]interface{}{"type": it[0], "text": it[1]})
	}
	return map[string]interface{}{
		"copyright": map[string]interface{}{"items": list},
	}
}

func TestJoinAlbumCopyright(t *testing.T) {
	tests := []struct {
		name  string
		album map[string]interface{}
		want  string
	}{
		{
			// The exact real-world shape this fix targets: Karma To Burn —
			// "19" (Alamo Records/Sony) carries only a "P" entry, no "C" at
			// all. The old code unconditionally dropped every "P" entry and
			// returned "" here, even though Spotify HAD supplied the data —
			// measured as the largest single cause (212/234) of tracks never
			// converging in the retag pass.
			name:  "P only falls back to the phonogram text",
			album: copyrightAlbum([2]string{"P", "(P) 2019 Alamo Records, LLC/Sony Music Entertainment"}),
			want:  "(P) 2019 Alamo Records, LLC/Sony Music Entertainment",
		},
		{
			// Both present with the SAME text (the common case — e.g. 1000mods
			// "Vidage": C="(C) 2011 1000mods", P="(P) 2011 1000mods"): must
			// still prefer C alone, not join both and duplicate the text.
			name:  "both present prefers C, does not duplicate",
			album: copyrightAlbum([2]string{"C", "2011 1000mods"}, [2]string{"P", "2011 1000mods"}),
			want:  "2011 1000mods",
		},
		{
			// Both present with DIFFERENT text (e.g. Superjava — Cocobongo:
			// C="2017 A Life In A Track", P="2017 Superjava") — must return
			// the C text, the pre-existing behavior this fix must not change.
			name:  "both present with different text still prefers C",
			album: copyrightAlbum([2]string{"C", "2017 A Life In A Track"}, [2]string{"P", "2017 Superjava"}),
			want:  "2017 A Life In A Track",
		},
		{
			name:  "multiple C entries are joined",
			album: copyrightAlbum([2]string{"C", "2020 Label A"}, [2]string{"C", "2020 Label B"}),
			want:  "2020 Label A, 2020 Label B",
		},
		{
			name:  "multiple P-only entries are joined",
			album: copyrightAlbum([2]string{"P", "2020 Label A"}, [2]string{"P", "2020 Label B"}),
			want:  "2020 Label A, 2020 Label B",
		},
		{
			name:  "no copyright block at all",
			album: map[string]interface{}{},
			want:  "",
		},
		{
			name:  "empty items list",
			album: copyrightAlbum(),
			want:  "",
		},
		{
			name:  "blank text is dropped",
			album: copyrightAlbum([2]string{"P", ""}),
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinAlbumCopyright(tt.album); got != tt.want {
				t.Errorf("joinAlbumCopyright(%+v) = %q, want %q", tt.album, got, tt.want)
			}
		})
	}
}
