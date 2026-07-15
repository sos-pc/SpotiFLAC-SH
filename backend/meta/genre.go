package meta

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Genre resolution, ordered by how well a source can answer "what genre is
// THIS recording?".
//
// Every tier matches on the ISRC, so none of them guess by name — the wrong
// remaster carrying the right title is a failure mode we do not accept here.
// What varies is how close each source sits to the track itself:
//
//	1. Apple Music — per-track, curated taxonomy. The precise answer.
//	2. Deezer      — per-ALBUM. Right for a normal album, coarse for a
//	                 compilation, but exact on the match and free of auth.
//	3. MusicBrainz — per-recording, but free-form folksonomy ("seen live",
//	                 "vinyl") and sparse: it is the reason the retag pass
//	                 fills so few genres (see R10). Kept last because it
//	                 occasionally knows underground records the other two
//	                 have never heard of.
//
// A tier that errors or simply has nothing hands over to the next. Genre is
// best-effort by design (the download never waits on it and never fails for
// it), so the chain returns an empty genre rather than an error when every
// source comes up dry.
const maxGenres = 5

type genreSource struct {
	name  string
	fetch func(isrc string) ([]string, error)
}

// genreChain is ordered; see the doc comment above for why.
var genreChain = []genreSource{
	{"apple", appleGenreNames},
	{"deezer", deezerGenreNames},
	{"musicbrainz", musicBrainzGenreNames},
}

// ResolveGenre walks the chain and returns the first genre found, plus the
// name of the source that supplied it (for logs and for telling "nobody knows
// this track" apart from "our best source is broken").
//
// Returns ("", "", nil) when no source has a genre — a normal outcome, not a
// failure.
func ResolveGenre(isrc string, useSingleGenre bool) (string, string, error) {
	isrc = strings.ToUpper(strings.TrimSpace(isrc))
	if isrc == "" {
		return "", "", nil
	}

	for _, src := range genreChain {
		names, err := src.fetch(isrc)
		if err != nil {
			// One source being down is not the caller's problem: say so once
			// and keep going. This is the line that surfaces "Apple changed
			// its bundle" in the logs; the status page reports it too.
			slog.Debug("[Genre] source failed", "source", src.name, "isrc", isrc, "err", err)
			continue
		}
		if genre := formatGenreNames(names, useSingleGenre); genre != "" {
			slog.Debug("[Genre] resolved", "source", src.name, "isrc", isrc, "genre", genre)
			return genre, src.name, nil
		}
	}

	slog.Debug("[Genre] no source had a genre", "isrc", isrc)
	return "", "", nil
}

// ResolveGenreFrom runs one named tier on its own. Health checks need this:
// asking the whole chain would go green off Deezer while Apple — the tier we
// actually care about watching — is dead.
func ResolveGenreFrom(source, isrc string) (string, string, error) {
	for _, src := range genreChain {
		if src.name != source {
			continue
		}
		names, err := src.fetch(strings.ToUpper(strings.TrimSpace(isrc)))
		if err != nil {
			return "", src.name, err
		}
		return formatGenreNames(names, false), src.name, nil
	}
	return "", "", fmt.Errorf("unknown genre source %q", source)
}

// formatGenreNames renders a source's genre list the way the rest of the app
// expects it, matching what FetchMusicBrainzMetadata has always produced:
// Title Case, de-duplicated, capped, joined with the configured separator —
// or just the first (most relevant) one when the user wants a single genre.
func formatGenreNames(names []string, useSingleGenre bool) string {
	caser := cases.Title(language.English)

	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = caser.String(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	if len(out) == 0 {
		return ""
	}
	if useSingleGenre {
		return out[0]
	}
	if len(out) > maxGenres {
		out = out[:maxGenres]
	}
	return strings.Join(out, util.Separator)
}
