package meta

import (
	"os"
	"path/filepath"
	"testing"

	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

func TestReadFullTrackTagsFlacRoundTrip(t *testing.T) {
	cmt := flacvorbis.New()
	for _, kv := range [][2]string{
		{"TITLE", "Midnight City"},
		{"ARTIST", "Synth Dreams"},
		{"ALBUM", "Neon Nights"},
		{"ALBUMARTIST", "Synth Dreams"},
		{"DATE", "2024-03-15"},
		{"TRACKNUMBER", "3"},
		{"DISCNUMBER", "1"},
		{"ISRC", "USRC17607839"},
		{"GENRE", "Synthwave"},
		{"COPYRIGHT", "2024 Some Label"},
		{SpotifyIDTagKey, "spotify:track:abc"},
	} {
		if err := cmt.Add(kv[0], kv[1]); err != nil {
			t.Fatalf("add %s comment: %v", kv[0], err)
		}
	}
	block := cmt.Marshal()

	// go-flac's readFLACStream indexes into the frame data unconditionally
	// (result[0], result[1]) with no length check — a real FLAC always has
	// audio frames after the metadata, but a synthetic metadata-only file
	// needs a fake minimal frame sync header (0xFF 0xF8) or ParseFile
	// panics on index-out-of-range (caught by safeParseFlac's recover(),
	// which would otherwise silently turn this into an empty read).
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}
	path := filepath.Join(t.TempDir(), "track.flac")
	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}

	got := ReadFullTrackTags(path)
	want := FullTrackTags{
		SpotifyID:   "spotify:track:abc",
		Title:       "Midnight City",
		Artist:      "Synth Dreams",
		Album:       "Neon Nights",
		AlbumArtist: "Synth Dreams",
		ReleaseDate: "2024-03-15",
		TrackNumber: 3,
		DiscNumber:  1,
		ISRC:        "USRC17607839",
		Genre:       "Synthwave",
		Copyright:   "2024 Some Label",
	}
	if got != want {
		t.Errorf("ReadFullTrackTags = %+v, want %+v", got, want)
	}
}

func TestReadFullTrackTagsFlacMissingTagsAreEmpty(t *testing.T) {
	f := &flac.File{}
	path := filepath.Join(t.TempDir(), "notags.flac")
	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}

	got := ReadFullTrackTags(path)
	if got != (FullTrackTags{}) {
		t.Errorf("ReadFullTrackTags on a tagless file = %+v, want zero value", got)
	}
}

func TestReadFullTrackTagsMp3RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("id3v2.Open: %v", err)
	}
	tag.SetTitle("Midnight City")
	tag.SetArtist("Synth Dreams")
	tag.SetAlbum("Neon Nights")
	tag.SetYear("2024")
	tag.AddTextFrame("TPE2", id3v2.EncodingUTF8, "Synth Dreams")
	tag.AddTextFrame(tag.CommonID("Track number/Position in set"), id3v2.EncodingUTF8, "3/12")
	tag.AddTextFrame(tag.CommonID("Part of a set"), id3v2.EncodingUTF8, "1")
	tag.AddTextFrame("TSRC", id3v2.EncodingUTF8, "USRC17607839")
	tag.AddTextFrame("TCON", id3v2.EncodingUTF8, "Synthwave")
	tag.AddTextFrame("TCOP", id3v2.EncodingUTF8, "2024 Some Label")
	tag.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{
		Encoding:    id3v2.EncodingUTF8,
		Description: SpotifyIDTagKey,
		Value:       "spotify:track:abc",
	})
	if err := tag.Save(); err != nil {
		t.Fatalf("tag.Save: %v", err)
	}
	tag.Close()

	got := ReadFullTrackTags(path)
	want := FullTrackTags{
		SpotifyID:   "spotify:track:abc",
		Title:       "Midnight City",
		Artist:      "Synth Dreams",
		Album:       "Neon Nights",
		AlbumArtist: "Synth Dreams",
		ReleaseDate: "2024",
		TrackNumber: 3,
		DiscNumber:  1,
		ISRC:        "USRC17607839",
		Genre:       "Synthwave",
		Copyright:   "2024 Some Label",
	}
	if got != want {
		t.Errorf("ReadFullTrackTags = %+v, want %+v", got, want)
	}
}

func TestReadFullTrackTagsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, []byte("not a real wav"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if got := ReadFullTrackTags(path); got != (FullTrackTags{}) {
		t.Errorf("ReadFullTrackTags on unsupported extension = %+v, want zero value", got)
	}
}

func TestReadFullTrackTagsMissingFileIsEmpty(t *testing.T) {
	got := ReadFullTrackTags(filepath.Join(t.TempDir(), "does-not-exist.flac"))
	if got != (FullTrackTags{}) {
		t.Errorf("ReadFullTrackTags on a missing file = %+v, want zero value", got)
	}
}

// TestEmbedMetadataToMP3WritesGenre is the regression test for MP3 never
// getting a GENRE tag at all: genre embedding used to be FLAC-only (a
// vorbis comment), so ReadFullTrackTags on an MP3 always came back empty
// regardless of settings. embedMetadataToMP3 now also writes a TCON frame.
func TestEmbedMetadataToMP3WritesGenre(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	if err := embedMetadataToMP3(path, Metadata{Genre: "Synthwave", ISRC: "USRC17607839"}, ""); err != nil {
		t.Fatalf("embedMetadataToMP3: %v", err)
	}

	got := ReadFullTrackTags(path)
	if got.Genre != "Synthwave" {
		t.Errorf("genre = %q, want %q", got.Genre, "Synthwave")
	}
	if got.ISRC != "USRC17607839" {
		t.Errorf("isrc = %q, want %q", got.ISRC, "USRC17607839")
	}
}

func TestParseLeadingInt(t *testing.T) {
	cases := map[string]int{
		"3":      3,
		"3/12":   3,
		" 3 ":    3,
		"":       0,
		"abc":    0,
		"0/12":   0,
		"12/abc": 12,
	}
	for input, want := range cases {
		if got := parseLeadingInt(input); got != want {
			t.Errorf("parseLeadingInt(%q) = %d, want %d", input, got, want)
		}
	}
}
