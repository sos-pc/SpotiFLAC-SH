package meta

import "testing"

// ─── Metadata struct validation ──────────────────────────────────────────────

func TestMetadata_ZeroValue(t *testing.T) {
	var m Metadata
	if m.Title != "" || m.Artist != "" || m.Album != "" {
		t.Error("zero value should have empty string fields")
	}
	if m.TrackNumber != 0 || m.DiscNumber != 0 {
		t.Error("zero value should have zero int fields")
	}
}

func TestMetadata_AllStringFields(t *testing.T) {
	m := Metadata{
		Title:       "Bohemian Rhapsody",
		Artist:      "Queen",
		Album:       "A Night at the Opera",
		AlbumArtist: "Queen",
		Date:        "1975-10-31",
		ReleaseDate: "1975-10-31",
		URL:         "https://open.spotify.com/track/example",
		Copyright:   "© 1975 Queen",
		Publisher:   "EMI",
		Lyrics:      "Is this the real life?",
		Description: "Classic rock anthem",
		ISRC:        "GBUM71029604",
		Genre:       "Rock",
	}

	if m.Title != "Bohemian Rhapsody" {
		t.Errorf("Title: got %q", m.Title)
	}
	if m.Artist != "Queen" {
		t.Errorf("Artist: got %q", m.Artist)
	}
	if m.ISRC != "GBUM71029604" {
		t.Errorf("ISRC: got %q", m.ISRC)
	}
	if m.Genre != "Rock" {
		t.Errorf("Genre: got %q", m.Genre)
	}
}

func TestMetadata_TrackAndDiscNumbers(t *testing.T) {
	m := Metadata{
		TrackNumber: 1,
		TotalTracks: 12,
		DiscNumber:  2,
		TotalDiscs:  2,
	}

	if m.TrackNumber != 1 {
		t.Errorf("TrackNumber: got %d", m.TrackNumber)
	}
	if m.TotalTracks != 12 {
		t.Errorf("TotalTracks: got %d", m.TotalTracks)
	}
	if m.DiscNumber != 2 {
		t.Errorf("DiscNumber: got %d", m.DiscNumber)
	}
	if m.TotalDiscs != 2 {
		t.Errorf("TotalDiscs: got %d", m.TotalDiscs)
	}
}

func TestMetadata_LyricsField(t *testing.T) {
	lrc := "[00:00.00] Line one\n[00:05.00] Line two"
	m := Metadata{Lyrics: lrc}
	if m.Lyrics != lrc {
		t.Errorf("Lyrics field should preserve content verbatim, got %q", m.Lyrics)
	}
}

func TestMetadata_Copy(t *testing.T) {
	orig := Metadata{Title: "Original", TrackNumber: 3}
	copy := orig
	copy.Title = "Copy"
	copy.TrackNumber = 99

	// orig must be unchanged (value semantics)
	if orig.Title != "Original" {
		t.Errorf("orig.Title mutated to %q", orig.Title)
	}
	if orig.TrackNumber != 3 {
		t.Errorf("orig.TrackNumber mutated to %d", orig.TrackNumber)
	}
}

func TestMetadata_EmptyLyrics(t *testing.T) {
	m := Metadata{Title: "Song", Lyrics: ""}
	if m.Lyrics != "" {
		t.Errorf("empty Lyrics should be empty string, got %q", m.Lyrics)
	}
}

func TestMetadata_UnicodeFields(t *testing.T) {
	m := Metadata{
		Title:  "二つの心臓",
		Artist: "Björk",
		Genre:  "Élektronique",
	}
	if m.Title != "二つの心臓" {
		t.Errorf("Unicode title not preserved: %q", m.Title)
	}
	if m.Artist != "Björk" {
		t.Errorf("Unicode artist not preserved: %q", m.Artist)
	}
}
