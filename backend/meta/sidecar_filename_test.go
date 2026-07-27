package meta

import "testing"

// The cover and lyrics builders were two near-identical copies. Merging them was
// only safe because the two places they disagreed became parameters — these
// tests pin that disagreement, so "harmonising" the separators later has to be a
// deliberate act with a migration behind it, not a tidy-up. Changing either value
// renames every sidecar already on disk.
func TestSidecarSeparatorsDifferPerCaller(t *testing.T) {
	const (
		track  = "bad guy"
		artist = "Billie Eilish"
	)
	cover := buildSidecarFilename(track, artist, "", "", "", "title-artist", true, 3, 0, coverTrackSeparator, ".jpg")
	lyrics := buildSidecarFilename(track, artist, "", "", "", "title-artist", true, 3, 0, lyricsTrackSeparator, ".lrc")

	if want := "03 - bad guy - Billie Eilish.jpg"; cover != want {
		t.Errorf("cover name changed\n got: %q\nwant: %q", cover, want)
	}
	// Note the dot: lyrics match how the audio file is numbered, cover does not.
	if want := "03. bad guy - Billie Eilish.lrc"; lyrics != want {
		t.Errorf("lyrics name changed\n got: %q\nwant: %q", lyrics, want)
	}
}

// "title-artist" was spelled out in the cover copy and absent from the lyrics
// one. It is dropped here because default already produces exactly that — this
// asserts the two really are equivalent, which is what made the removal safe.
func TestTitleArtistMatchesDefault(t *testing.T) {
	explicit := buildSidecarFilename("T", "A", "", "", "", "title-artist", false, 0, 0, coverTrackSeparator, ".jpg")
	fallback := buildSidecarFilename("T", "A", "", "", "", "something-unknown", false, 0, 0, coverTrackSeparator, ".jpg")
	if explicit != fallback {
		t.Errorf("title-artist (%q) and default (%q) diverged — the dropped case was not redundant", explicit, fallback)
	}
}

// The separator applies to the plain presets only: a "{...}" template positions
// the number itself via {track}, so passing one must not prepend anything.
func TestTemplateFormatIgnoresSeparator(t *testing.T) {
	got := buildSidecarFilename("T", "A", "Alb", "AA", "2019-03-29", "{track}. {title}", true, 7, 0, coverTrackSeparator, ".jpg")
	if want := "07. T.jpg"; got != want {
		t.Errorf("template output = %q, want %q", got, want)
	}
}

func TestNoTrackNumberWhenNotRequested(t *testing.T) {
	got := buildSidecarFilename("T", "A", "", "", "", "title", false, 5, 0, lyricsTrackSeparator, ".lrc")
	if want := "T.lrc"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
