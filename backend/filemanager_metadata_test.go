package backend

import (
	"path/filepath"
	"testing"

	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// The File Manager exists to answer "what is actually tagged on this file?".
// Genre and ISRC are the two tags most likely to be missing — they are what a
// retag backfills — so a reader that silently drops them is worse than useless
// there. These drive the real FLAC reader over a real file.

// writeTestFlac builds a synthetic FLAC carrying the given Vorbis comments.
// The fake frame sync header is required: go-flac's reader indexes into the
// frame data with no length check, so a metadata-only file makes ParseFile
// panic (see the same note in backend/meta/track_tags_test.go).
func writeTestFlac(t *testing.T, comments [][2]string) string {
	t.Helper()

	cmt := flacvorbis.New()
	for _, kv := range comments {
		if err := cmt.Add(kv[0], kv[1]); err != nil {
			t.Fatalf("add %s: %v", kv[0], err)
		}
	}
	block := cmt.Marshal()
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}

	path := filepath.Join(t.TempDir(), "track.flac")
	if err := f.Save(path); err != nil {
		t.Fatalf("save flac: %v", err)
	}
	return path
}

func TestReadAudioMetadata_FLAC_ReadsGenreAndISRC(t *testing.T) {
	path := writeTestFlac(t, [][2]string{
		{"TITLE", "One More Time"},
		{"ARTIST", "Daft Punk"},
		{"ALBUM", "Discovery"},
		{"GENRE", "Dance, House, Electronic"},
		{"ISRC", "GBDUW0000053"},
	})

	got, err := ReadAudioMetadata(path)
	if err != nil {
		t.Fatalf("ReadAudioMetadata: %v", err)
	}
	if got.Genre != "Dance, House, Electronic" {
		t.Errorf("Genre = %q, want the tag we wrote", got.Genre)
	}
	if got.ISRC != "GBDUW0000053" {
		t.Errorf("ISRC = %q, want GBDUW0000053", got.ISRC)
	}
	// The fields that already worked must keep working.
	if got.Title != "One More Time" || got.Artist != "Daft Punk" || got.Album != "Discovery" {
		t.Errorf("regressed on existing fields: %+v", got)
	}
}

func TestReadAudioMetadata_FLAC_UntaggedStaysEmpty(t *testing.T) {
	// An untagged file must report empty rather than invent a value: the File
	// Manager showing a genre that is not on disk would be a lie, and this
	// reader is how you check whether a retag actually wrote one.
	path := writeTestFlac(t, [][2]string{{"TITLE", "Untitled"}})

	got, err := ReadAudioMetadata(path)
	if err != nil {
		t.Fatalf("ReadAudioMetadata: %v", err)
	}
	if got.Genre != "" || got.ISRC != "" {
		t.Errorf("Genre=%q ISRC=%q, want both empty", got.Genre, got.ISRC)
	}
}
