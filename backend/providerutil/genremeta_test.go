package providerutil

import (
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

func TestFetchGenreMetadataAsyncClosedWhenGenreNotRequested(t *testing.T) {
	ch := FetchGenreMetadataAsync("USRC17607839", "", "Title", "Artist", "Album", false, false)
	res, ok := <-ch
	if ok {
		t.Errorf("expected a closed empty channel when embedGenre is false, got a value: %+v", res)
	}
}

func TestFetchGenreMetadataAsyncClosedWhenNothingToResolveFrom(t *testing.T) {
	ch := FetchGenreMetadataAsync("", "", "Title", "Artist", "Album", false, true)
	res, ok := <-ch
	if ok {
		t.Errorf("expected a closed empty channel when isrc and spotifyURL are both empty, got a value: %+v", res)
	}
}

// TestFetchGenreMetadataAsyncAlwaysSendsExactlyOneResult exercises the real
// resolution path (a pre-known ISRC, so no network call is needed to
// derive one) end to end. The MusicBrainz lookup itself will fail in this
// sandboxed test environment (no network access) — that's fine and
// expected; what's being asserted is the channel contract: the reader
// below must not block forever waiting for a result that never arrives.
func TestFetchGenreMetadataAsyncAlwaysSendsExactlyOneResult(t *testing.T) {
	ch := FetchGenreMetadataAsync("USRC17607839", "", "Title", "Artist", "Album", false, true)
	res := <-ch
	if res.ISRC != "USRC17607839" {
		t.Errorf("ISRC = %q, want the pre-known ISRC to be echoed back unchanged", res.ISRC)
	}
}

// genreForWriting is where a chain result becomes a value that gets written
// to files/catalog — this is what applies the "give it a real Unknown label
// instead of leaving it blank" decision from R10's genre discussion, so it
// must apply to every caller (live downloads and the retag pass alike) via
// this single shared point.
func TestGenreForWriting(t *testing.T) {
	tests := []struct {
		name string
		in   meta.GenreResult
		want string
	}{
		{
			name: "found genre passes through unchanged",
			in:   meta.GenreResult{Genre: "Dance", Source: "apple", Outcome: meta.GenreFound},
			want: "Dance",
		},
		{
			// The case this whole mechanism exists for: every source
			// answered, none had a genre for this exact recording.
			name: "unknown becomes the sentinel",
			in:   meta.GenreResult{Outcome: meta.GenreUnknown},
			want: util.UnknownGenre,
		},
		{
			// OUR bug (ISRC never resolved), not a fact about the world —
			// must stay blank so the selection clause's plain `genre = ''`
			// keeps catching it without any special-casing.
			name: "no isrc stays blank",
			in:   meta.GenreResult{Outcome: meta.GenreNoISRC},
			want: "",
		},
		{
			// OUR bug (every source errored) — same reasoning: stays blank,
			// not the sentinel, so a real outage doesn't get mistaken for
			// "we checked and there's nothing".
			name: "every source failing stays blank",
			in:   meta.GenreResult{Outcome: meta.GenreFailed},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := genreForWriting(tt.in); got != tt.want {
				t.Errorf("genreForWriting(%+v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveISRCFromSpotifyURLHandlesMalformedInput(t *testing.T) {
	tests := []string{"", "not-a-url", "https://open.spotify.com/track/"}
	for _, in := range tests {
		// Must not panic on any malformed input — the real regression this
		// guards is a nil/empty-slice index panic when splitting a
		// malformed URL, which previously ran unguarded inline in three
		// separate provider clients.
		_ = resolveISRCFromSpotifyURL(in)
	}
}
