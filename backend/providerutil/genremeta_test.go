package providerutil

import "testing"

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
