package main

import (
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// genreIsMissing backs both the selection counter below and retagOneTrack's
// skip-guard — util.UnknownGenre must count as missing in both, or a track we
// once gave up on would silently stop being retried forever, the opposite of
// what it is meant to mean (see util.UnknownGenre's doc comment).
func TestGenreIsMissing(t *testing.T) {
	tests := []struct {
		genre string
		want  bool
	}{
		{"", true},
		{util.UnknownGenre, true},
		{"Rock", false},
		{"Dance, House", false},
	}
	for _, tt := range tests {
		if got := genreIsMissing(tt.genre); got != tt.want {
			t.Errorf("genreIsMissing(%q) = %v, want %v", tt.genre, got, tt.want)
		}
	}
}

// These counters are the whole point of the instrumentation: they are what
// turns "skipped=2534" into a decision about R10. If they miscount, the
// decision is wrong, so pin the bucketing.

func TestCountGenre_SortsEachOutcomeIntoItsOwnBucket(t *testing.T) {
	var r retagIncompleteMetadataResult

	r.countGenre(genreDiag{asked: true, source: "apple", outcome: meta.GenreFound})
	r.countGenre(genreDiag{asked: true, source: "apple", outcome: meta.GenreFound})
	r.countGenre(genreDiag{asked: true, source: "deezer", outcome: meta.GenreFound})
	r.countGenre(genreDiag{asked: true, outcome: meta.GenreUnknown})
	r.countGenre(genreDiag{asked: true, outcome: meta.GenreNoISRC})
	r.countGenre(genreDiag{asked: true, outcome: meta.GenreFailed})
	r.countGenre(genreDiag{}) // never asked

	if got := r.GenreBySource["apple"]; got != 2 {
		t.Errorf("apple = %d, want 2", got)
	}
	if got := r.GenreBySource["deezer"]; got != 1 {
		t.Errorf("deezer = %d, want 1", got)
	}
	if r.GenreUnknown != 1 || r.GenreNoISRC != 1 || r.GenreFailed != 1 || r.GenreAlready != 1 {
		t.Errorf("unknown=%d no_isrc=%d failed=%d not_asked=%d, want 1 each",
			r.GenreUnknown, r.GenreNoISRC, r.GenreFailed, r.GenreAlready)
	}
}

// "Nobody has a genre for this" and "our sources were down" must never land in
// the same bucket — the first calls for the selection-clause fix, the second
// for a bug hunt.
func TestCountGenre_KeepsUnknownApartFromFailed(t *testing.T) {
	var r retagIncompleteMetadataResult
	for i := 0; i < 3; i++ {
		r.countGenre(genreDiag{asked: true, outcome: meta.GenreUnknown})
	}
	r.countGenre(genreDiag{asked: true, outcome: meta.GenreFailed})

	if r.GenreUnknown != 3 || r.GenreFailed != 1 {
		t.Errorf("unknown=%d failed=%d, want 3 and 1", r.GenreUnknown, r.GenreFailed)
	}
}

func TestCountGenre_EveryTrackLandsInExactlyOneBucket(t *testing.T) {
	var r retagIncompleteMetadataResult
	diags := []genreDiag{
		{asked: true, source: "apple", outcome: meta.GenreFound},
		{asked: true, source: "musicbrainz", outcome: meta.GenreFound},
		{asked: true, outcome: meta.GenreUnknown},
		{asked: true, outcome: meta.GenreNoISRC},
		{asked: true, outcome: meta.GenreFailed},
		{},
	}
	for _, d := range diags {
		r.countGenre(d)
	}

	total := r.GenreUnknown + r.GenreNoISRC + r.GenreFailed + r.GenreAlready
	for _, n := range r.GenreBySource {
		total += n
	}
	if total != len(diags) {
		t.Errorf("buckets sum to %d, want %d — a track was dropped or double-counted", total, len(diags))
	}
}

func TestCountGenre_NilMapIsSafe(t *testing.T) {
	var r retagIncompleteMetadataResult // GenreBySource is nil
	r.countGenre(genreDiag{asked: true, source: "apple", outcome: meta.GenreFound})
	if r.GenreBySource["apple"] != 1 {
		t.Error("counting into a nil map must not panic or lose the count")
	}
}

// The selection-reason counter is what turned "just drop genre from the clause"
// (the audit's guess) into a measured decision: a track re-selected on an empty
// copyright, with genre already present, proves dropping genre would not help
// it. Pin that a track counts once per empty field it actually has.
func TestCountSelectionReason_CountsEveryEmptyField(t *testing.T) {
	var r retagIncompleteMetadataResult

	// Complete except copyright: the clause's blind spot — nothing the pass
	// fetches supplies it, so this track comes back every run.
	r.countSelectionReason(db.Track{
		ISRC: "X", Name: "n", ArtistName: "a", TrackNumber: 1, DiscNumber: 1,
		DurationMs: 1000, Genre: "Rock", ReleaseDate: "2020", AlbumName: "al",
		AlbumArtist: "aa", CoverURL: "u", Copyright: "",
	})
	if r.SelectedByField["copyright"] != 1 {
		t.Fatalf("copyright = %d, want 1", r.SelectedByField["copyright"])
	}
	for _, f := range []string{"genre", "isrc", "name", "album_name"} {
		if r.SelectedByField[f] != 0 {
			t.Errorf("%s = %d, want 0 (it was present)", f, r.SelectedByField[f])
		}
	}

	// A track missing several fields bumps several counters — this is "how
	// often is each field a reason", not a one-bucket-per-track partition.
	r.countSelectionReason(db.Track{Genre: "", Copyright: "", DurationMs: 0,
		ISRC: "X", Name: "n", ArtistName: "a", TrackNumber: 1, DiscNumber: 1,
		ReleaseDate: "2020", AlbumName: "al", AlbumArtist: "aa", CoverURL: "u"})
	if r.SelectedByField["copyright"] != 2 || r.SelectedByField["genre"] != 1 || r.SelectedByField["duration_ms"] != 1 {
		t.Errorf("got copyright=%d genre=%d duration_ms=%d, want 2/1/1",
			r.SelectedByField["copyright"], r.SelectedByField["genre"], r.SelectedByField["duration_ms"])
	}

	// A track marked with the sentinel must count as a genre reason too — it
	// is exactly as "missing" as blank for retry purposes (see genreIsMissing).
	r.countSelectionReason(db.Track{Genre: util.UnknownGenre,
		ISRC: "X", Name: "n", ArtistName: "a", TrackNumber: 1, DiscNumber: 1,
		DurationMs: 1000, ReleaseDate: "2020", AlbumName: "al", AlbumArtist: "aa",
		CoverURL: "u", Copyright: "c"})
	if r.SelectedByField["genre"] != 2 {
		t.Errorf("genre = %d, want 2 (was 1, this track bumps it once more) — the sentinel-tagged track must count as a genre reason too", r.SelectedByField["genre"])
	}
}
