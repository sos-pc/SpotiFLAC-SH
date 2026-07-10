package meta

import (
	"os"
	"path/filepath"
	"testing"

	id3v2 "github.com/bogem/id3v2/v2"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

func TestReadTrackTagsFlacRoundTrip(t *testing.T) {
	cmt := flacvorbis.New()
	if err := cmt.Add("ISRC", "USRC17607839"); err != nil {
		t.Fatalf("add ISRC comment: %v", err)
	}
	if err := cmt.Add("GENRE", "Synthwave"); err != nil {
		t.Fatalf("add GENRE comment: %v", err)
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

	isrc, genre := ReadTrackTags(path)
	if isrc != "USRC17607839" {
		t.Errorf("isrc = %q, want %q", isrc, "USRC17607839")
	}
	if genre != "Synthwave" {
		t.Errorf("genre = %q, want %q", genre, "Synthwave")
	}
}

func TestReadTrackTagsFlacMissingTagsAreEmpty(t *testing.T) {
	f := &flac.File{}
	path := filepath.Join(t.TempDir(), "notags.flac")
	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}

	isrc, genre := ReadTrackTags(path)
	if isrc != "" || genre != "" {
		t.Errorf("ReadTrackTags on a tagless file = (%q, %q), want (\"\", \"\")", isrc, genre)
	}
}

func TestReadTrackTagsMp3RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("id3v2.Open: %v", err)
	}
	tag.AddTextFrame("TSRC", id3v2.EncodingUTF8, "USRC17607839")
	tag.AddTextFrame("TCON", id3v2.EncodingUTF8, "Synthwave")
	if err := tag.Save(); err != nil {
		t.Fatalf("tag.Save: %v", err)
	}
	tag.Close()

	isrc, genre := ReadTrackTags(path)
	if isrc != "USRC17607839" {
		t.Errorf("isrc = %q, want %q", isrc, "USRC17607839")
	}
	if genre != "Synthwave" {
		t.Errorf("genre = %q, want %q", genre, "Synthwave")
	}
}

func TestReadTrackTagsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, []byte("not a real wav"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	isrc, genre := ReadTrackTags(path)
	if isrc != "" || genre != "" {
		t.Errorf("ReadTrackTags on unsupported extension = (%q, %q), want (\"\", \"\")", isrc, genre)
	}
}

func TestReadTrackTagsMissingFileIsEmpty(t *testing.T) {
	isrc, genre := ReadTrackTags(filepath.Join(t.TempDir(), "does-not-exist.flac"))
	if isrc != "" || genre != "" {
		t.Errorf("ReadTrackTags on a missing file = (%q, %q), want (\"\", \"\")", isrc, genre)
	}
}

// TestEmbedMetadataToMP3WritesGenre is the regression test for MP3 never
// getting a GENRE tag at all: genre embedding used to be FLAC-only (a
// vorbis comment), so ReadTrackTags on an MP3 always came back empty
// regardless of settings. embedMetadataToMP3 now also writes a TCON frame.
func TestEmbedMetadataToMP3WritesGenre(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0644); err != nil {
		t.Fatalf("seed empty file: %v", err)
	}

	if err := embedMetadataToMP3(path, Metadata{Genre: "Synthwave", ISRC: "USRC17607839"}, ""); err != nil {
		t.Fatalf("embedMetadataToMP3: %v", err)
	}

	isrc, genre := ReadTrackTags(path)
	if genre != "Synthwave" {
		t.Errorf("genre = %q, want %q", genre, "Synthwave")
	}
	if isrc != "USRC17607839" {
		t.Errorf("isrc = %q, want %q", isrc, "USRC17607839")
	}
}
