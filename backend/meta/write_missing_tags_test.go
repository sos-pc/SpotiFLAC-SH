package meta

import (
	"path/filepath"
	"testing"

	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// writeTestFlacTags builds a minimal but real-enough FLAC file with the
// given vorbis comments — see track_tags_test.go's
// TestReadFullTrackTagsFlacRoundTrip for why the fake frame-sync header is
// needed (go-flac's readFLACStream indexes unconditionally into frame data).
func writeTestFlacTags(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	cmt := flacvorbis.New()
	for k, v := range kv {
		if err := cmt.Add(k, v); err != nil {
			t.Fatalf("add %s comment: %v", k, err)
		}
	}
	block := cmt.Marshal()
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}
	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}
}

// TestWriteMissingTagsFillsOnlyEmptyFields is the core regression test:
// fields already present in the file (TITLE, ARTIST below) must survive
// completely unchanged even though fresh disagrees with them, while fields
// that were genuinely empty (ISRC, ALBUM, GENRE, COPYRIGHT) get filled in.
func TestWriteMissingTagsFillsOnlyEmptyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeTestFlacTags(t, path, map[string]string{
		"TITLE":  "Original Title",
		"ARTIST": "Original Artist",
	})

	current := ReadFullTrackTags(path)
	fresh := FullTrackTags{
		Title:       "Fresh Title",  // already present — must NOT overwrite
		Artist:      "Fresh Artist", // already present — must NOT overwrite
		Album:       "Fresh Album",  // missing — should fill
		AlbumArtist: "Fresh Album Artist",
		ReleaseDate: "2024-01-01",
		ISRC:        "USRC17607839",
		Genre:       "Synthwave",
		Copyright:   "2024 Some Label",
		TrackNumber: 5,
		DiscNumber:  1,
	}

	changed, err := WriteMissingTags(path, current, fresh)
	if err != nil {
		t.Fatalf("WriteMissingTags: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true (several fields were missing)")
	}

	got := ReadFullTrackTags(path)
	if got.Title != "Original Title" {
		t.Errorf("Title = %q, want unchanged %q", got.Title, "Original Title")
	}
	if got.Artist != "Original Artist" {
		t.Errorf("Artist = %q, want unchanged %q", got.Artist, "Original Artist")
	}
	if got.Album != "Fresh Album" || got.AlbumArtist != "Fresh Album Artist" ||
		got.ReleaseDate != "2024-01-01" || got.ISRC != "USRC17607839" ||
		got.Genre != "Synthwave" || got.Copyright != "2024 Some Label" ||
		got.TrackNumber != 5 || got.DiscNumber != 1 {
		t.Errorf("missing fields not filled correctly, got %+v", got)
	}
}

// TestWriteMissingTagsReturnsFalseWhenNothingToFill covers both "file
// already complete" and "fresh has nothing new to offer" — neither should
// touch the file.
func TestWriteMissingTagsReturnsFalseWhenNothingToFill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeTestFlacTags(t, path, map[string]string{
		"TITLE":  "Title",
		"ARTIST": "Artist",
	})
	current := ReadFullTrackTags(path)

	// fresh is entirely empty — nothing to offer regardless of what's missing.
	changed, err := WriteMissingTags(path, current, FullTrackTags{})
	if err != nil {
		t.Fatalf("WriteMissingTags: %v", err)
	}
	if changed {
		t.Error("changed = true, want false — fresh had nothing to offer")
	}

	got := ReadFullTrackTags(path)
	if got != current {
		t.Errorf("file was modified despite changed=false: got %+v, want unchanged %+v", got, current)
	}
}

// TestWriteMissingTagsDoesNotOverwriteNonZeroTrackOrDiscNumber verifies the
// numeric fields use the same "only if currently zero" rule as the string
// fields, not an unconditional overwrite.
func TestWriteMissingTagsDoesNotOverwriteNonZeroTrackOrDiscNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.flac")
	writeTestFlacTags(t, path, map[string]string{
		"DISCNUMBER": "2",
		// TRACKNUMBER deliberately absent — should get filled.
	})
	current := ReadFullTrackTags(path)
	fresh := FullTrackTags{TrackNumber: 7, DiscNumber: 9}

	changed, err := WriteMissingTags(path, current, fresh)
	if err != nil {
		t.Fatalf("WriteMissingTags: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true — TRACKNUMBER was missing")
	}

	got := ReadFullTrackTags(path)
	if got.TrackNumber != 7 {
		t.Errorf("TrackNumber = %d, want filled to %d", got.TrackNumber, 7)
	}
	if got.DiscNumber != 2 {
		t.Errorf("DiscNumber = %d, want unchanged %d (was already set)", got.DiscNumber, 2)
	}
}

func TestWriteMissingTagsRejectsNonFlac(t *testing.T) {
	path := filepath.Join(t.TempDir(), "track.mp3")
	if _, err := WriteMissingTags(path, FullTrackTags{}, FullTrackTags{Title: "X"}); err == nil {
		t.Error("expected an error for a non-FLAC path, got nil")
	}
}
