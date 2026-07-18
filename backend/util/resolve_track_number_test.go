package util

import "testing"

// ResolveTrackNumber exists because five call sites used to make this choice
// independently and disagreed, so a file was written under one name and looked
// for under another — and re-downloaded forever.
func TestResolveTrackNumber(t *testing.T) {
	tests := []struct {
		name                string
		listPosition        int
		albumTrackNumber    int
		useAlbumTrackNumber bool
		want                int
	}{
		{"album layout uses the album number", 7, 3, true, 3},
		{"flat layout keeps the list position", 7, 3, false, 7},
		{"album layout without a real album number falls back", 7, 0, true, 7},
		{"single track with no position takes the album number", 0, 5, true, 5},
		{"nothing to print at all", 0, 0, true, 0},
		{"flat layout, no position", 0, 5, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveTrackNumber(tc.listPosition, tc.albumTrackNumber, tc.useAlbumTrackNumber); got != tc.want {
				t.Errorf("ResolveTrackNumber(%d, %d, %v) = %d, want %d",
					tc.listPosition, tc.albumTrackNumber, tc.useAlbumTrackNumber, got, tc.want)
			}
		})
	}
}

// The invariant that actually matters: whatever number a caller resolves, the
// filename builder must print exactly that one. A builder that re-decided, or
// that guarded on a different value than it printed, is how a download and its
// existence check ended up disagreeing.
func TestBuildExpectedFilenameUsesTheResolvedNumber(t *testing.T) {
	const format = "{track}. {title} - {artist}"

	// Album layout: position 7 in the list, track 3 on its album.
	albumLayout := BuildExpectedFilename("Title", "Artist", "Album", "Artist", "2020-01-01",
		format, "", "", true, ResolveTrackNumber(7, 3, true), 1)
	if want := "03. Title - Artist.flac"; albumLayout != want {
		t.Errorf("album layout = %q, want %q", albumLayout, want)
	}

	// Flat layout, same track: the list position wins.
	flatLayout := BuildExpectedFilename("Title", "Artist", "Album", "Artist", "2020-01-01",
		format, "", "", true, ResolveTrackNumber(7, 3, false), 1)
	if want := "07. Title - Artist.flac"; flatLayout != want {
		t.Errorf("flat layout = %q, want %q", flatLayout, want)
	}

	// A single-track download has no list position. The number must still be
	// printed from the album number — this is the case where the old guard
	// tested the raw position, found 0, and silently printed nothing.
	singleTrack := BuildExpectedFilename("Title", "Artist", "Album", "Artist", "2020-01-01",
		"title-artist", "", "", true, ResolveTrackNumber(0, 5, true), 1)
	if want := "05. Title - Artist.flac"; singleTrack != want {
		t.Errorf("single track = %q, want %q", singleTrack, want)
	}
}
