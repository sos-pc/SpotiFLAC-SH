package main

import (
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

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
