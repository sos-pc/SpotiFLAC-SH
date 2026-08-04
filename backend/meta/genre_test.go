package meta

import (
	"errors"
	"strings"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// swapChain installs a fake chain for one test and restores the real one after.
// Lets us pin the ordering and fallback rules without touching the network.
func swapChain(t *testing.T, sources ...genreSource) {
	t.Helper()
	original := genreChain
	genreChain = sources
	t.Cleanup(func() { genreChain = original })
}

func src(name string, names []string, err error) genreSource {
	return genreSource{name: name, fetch: func(string) ([]string, error) { return names, err }}
}

func TestResolveGenre_PrefersTheEarlierTier(t *testing.T) {
	swapChain(t,
		src("apple", []string{"Dance"}, nil),
		src("deezer", []string{"Electro"}, nil),
	)
	got := ResolveGenre("GBDUW0000053", true)
	if got.Genre != "Dance" || got.Source != "apple" || got.Outcome != GenreFound {
		t.Errorf("got %+v, want Dance/apple/found — a later tier overtook an earlier one", got)
	}
}

func TestResolveGenre_FallsThroughOnError(t *testing.T) {
	swapChain(t,
		src("apple", nil, errors.New("bundle changed")),
		src("deezer", []string{"Electro"}, nil),
	)
	got := ResolveGenre("GBDUW0000053", true)
	if got.Genre != "Electro" || got.Source != "deezer" || got.Outcome != GenreFound {
		t.Errorf("got %+v, want Electro/deezer/found", got)
	}
}

func TestResolveGenre_FallsThroughOnEmpty(t *testing.T) {
	// A miss (track absent from a catalog) must behave like an error: hand over.
	swapChain(t,
		src("apple", nil, nil),
		src("deezer", []string{}, nil),
		src("musicbrainz", []string{"Trip Hop"}, nil),
	)
	got := ResolveGenre("GBDUW0000053", true)
	if got.Genre != "Trip Hop" || got.Source != "musicbrainz" || got.Outcome != GenreFound {
		t.Errorf("got %+v, want Trip Hop/musicbrainz/found", got)
	}
}

// The distinction the whole instrumentation exists for: every source answering
// "I don't know" is a fact about the world, and every source being down is our
// bug. They must not report the same outcome.
func TestResolveGenre_NobodyKnowsIsNotEverybodyBroken(t *testing.T) {
	swapChain(t,
		src("apple", nil, nil),
		src("deezer", []string{}, nil),
	)
	if got := ResolveGenre("GBDUW0000053", true); got.Outcome != GenreUnknown {
		t.Errorf("all sources answered and none knew: got %q, want %q", got.Outcome, GenreUnknown)
	}

	swapChain(t,
		src("apple", nil, errors.New("down")),
		src("deezer", nil, errors.New("down")),
	)
	if got := ResolveGenre("GBDUW0000053", true); got.Outcome != GenreFailed {
		t.Errorf("every source errored: got %q, want %q", got.Outcome, GenreFailed)
	}
}

func TestResolveGenre_OneAnswerMakesItUnknownNotFailed(t *testing.T) {
	// Mixed: apple broke, deezer genuinely had nothing. Someone did answer,
	// so this is "nobody knows", not "we are broken".
	swapChain(t,
		src("apple", nil, errors.New("down")),
		src("deezer", nil, nil),
	)
	if got := ResolveGenre("GBDUW0000053", true); got.Outcome != GenreUnknown {
		t.Errorf("got %q, want %q", got.Outcome, GenreUnknown)
	}
}

func TestResolveGenre_EmptyISRCSkipsTheNetwork(t *testing.T) {
	swapChain(t, genreSource{"apple", func(string) ([]string, error) {
		t.Fatal("no ISRC: the chain must not call any source")
		return nil, nil
	}})
	got := ResolveGenre("   ", true)
	if got.Genre != "" || got.Outcome != GenreNoISRC {
		t.Errorf("got %+v, want empty/no_isrc", got)
	}
}

func TestResolveGenreFrom_TargetsOneTier(t *testing.T) {
	swapChain(t,
		src("apple", nil, errors.New("bundle changed")),
		src("deezer", []string{"Electro"}, nil),
	)
	// The whole point: asking for apple must report apple's failure, not go
	// green off deezer.
	if _, _, err := ResolveGenreFrom("apple", "GBDUW0000053"); err == nil {
		t.Error("want apple's error to surface, got nil")
	}
	if genre, _, err := ResolveGenreFrom("deezer", "GBDUW0000053"); err != nil || genre != "Electro" {
		t.Errorf("got (%q, %v), want (Electro, nil)", genre, err)
	}
	if _, _, err := ResolveGenreFrom("nope", "GBDUW0000053"); err == nil {
		t.Error("want an error for an unknown source")
	}
}

func TestFormatGenreNames(t *testing.T) {
	tests := []struct {
		name   string
		in     []string
		single bool
		want   string
	}{
		{"title cases", []string{"hip-hop/rap"}, true, "Hip-Hop/Rap"},
		{"single takes the most relevant", []string{"Dance", "House", "Rock"}, true, "Dance"},
		{"multi joins", []string{"Dance", "House"}, false, "Dance" + util.Separator + "House"},
		{"dedups case-insensitively", []string{"Rock", "rock", "ROCK"}, false, "Rock"},
		{"drops blanks", []string{"", "  ", "Jazz"}, true, "Jazz"},
		{"nothing in, nothing out", nil, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatGenreNames(tt.in, tt.single); got != tt.want {
				t.Errorf("formatGenreNames(%v, %v) = %q, want %q", tt.in, tt.single, got, tt.want)
			}
		})
	}
}

func TestFormatGenreNames_CapsTheList(t *testing.T) {
	in := []string{"A", "B", "C", "D", "E", "F", "G"}
	got := formatGenreNames(in, false)
	if n := len(strings.Split(got, util.Separator)); n != maxGenres {
		t.Errorf("got %d genres (%q), want the list capped at %d", n, got, maxGenres)
	}
}
