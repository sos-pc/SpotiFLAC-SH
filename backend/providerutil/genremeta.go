package providerutil

import (
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/isrclookup"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// MBResult is the outcome of an async genre-metadata lookup: the ISRC that
// was used (resolved from spotifyURL if one wasn't already known) and
// whatever genre metadata was found for it.
//
// Source and Outcome exist so a caller counting results can tell apart the
// several different things that all look like "no genre" from the outside —
// see meta.GenreOutcome. The retag pass (R10) is the one that needs it.
type MBResult struct {
	ISRC     string
	Metadata meta.Metadata
	Source   string // genre tier that answered; "" if none did
	Outcome  meta.GenreOutcome
}

// FetchGenreMetadataAsync starts (in a panic-safe goroutine) resolving
// genre/MusicBrainz metadata for a track and returns a channel that always
// receives exactly one result. Every provider client used to hand-roll
// this exact goroutine+channel+recover shape independently — three of them
// (Tidal x2, Amazon) also independently re-derived the ISRC from
// spotifyURL with their own lookup instead of reusing one already known by
// the caller, which both duplicated the logic and spent an extra
// third-party call for a result the caller may already have.
//
// If isrc is already known (e.g. Qobuz resolves it earlier via its own
// ISRC-based search and has no need to re-derive it), pass it directly and
// spotifyURL can be left empty. Otherwise pass spotifyURL and isrc empty;
// the Spotify track ID is extracted from it and resolved to an ISRC via
// backend/isrclookup.
//
// Returns a closed, empty channel immediately (no goroutine spawned) if
// embedGenre is false or there's nothing to resolve from (isrc and
// spotifyURL both empty) — matching every call site's existing
// close(metaChan) fast path for "genre embedding isn't requested."
func FetchGenreMetadataAsync(isrc, spotifyURL, trackTitle, artistName, albumTitle string, useSingleGenre, embedGenre bool) <-chan MBResult {
	ch := make(chan MBResult, 1)
	if !embedGenre || (isrc == "" && spotifyURL == "") {
		close(ch)
		return ch
	}

	util.SafeGoOrElse("providerutil.fetchGenreMetadata", func() {
		resolvedISRC := isrc
		if resolvedISRC == "" {
			resolvedISRC = resolveISRCFromSpotifyURL(spotifyURL)
		}
		res := MBResult{ISRC: resolvedISRC, Outcome: meta.GenreNoISRC}
		if resolvedISRC != "" {
			// Apple -> Deezer -> MusicBrainz, every tier matched on the ISRC.
			// See meta/genre.go for why that order. MusicBrainz alone used to
			// answer here, and it knows a genre for only a minority of tracks
			// — the reason the retag pass fills so few (R10).
			got := meta.ResolveGenre(resolvedISRC, useSingleGenre)
			res.Metadata.Genre = genreForWriting(got)
			res.Source = got.Source
			res.Outcome = got.Outcome
		}
		ch <- res
	}, func() { ch <- MBResult{} })

	return ch
}

// genreForWriting turns a chain result into the genre string that should
// actually be written to the file/catalog. GenreUnknown becomes the explicit
// util.UnknownGenre sentinel (every source answered; none knew this
// recording — a fact worth recording, not a blank to leave hanging).
//
// GenreNoISRC and GenreFailed stay blank on purpose: those are OUR problems
// (an unresolved ISRC, a broken source), not a fact about the world, and the
// selection clause already re-picks a blank genre on the very next pass — no
// sentinel needed there, and writing one would hide a real bug behind a
// value that reads as "we checked and there's nothing".
func genreForWriting(got meta.GenreResult) string {
	if got.Outcome == meta.GenreUnknown {
		return util.UnknownGenre
	}
	return got.Genre
}

// resolveISRCFromSpotifyURL extracts the Spotify track ID from a Spotify
// track URL (the last path segment, query string stripped) and resolves its
// ISRC. Returns "" if the URL can't be parsed or no ISRC is found.
//
// Uses isrclookup.Resolve, which reads Spotify's own catalog record for this
// exact track and caches it. It replaced a Song.link lookup here in item 13:
// the direct one is authoritative instead of relying on a third-party
// aggregator being up, and it shares the ISRC cache the rest of the job
// pipeline already fills (jobs_helpers.go), so a genre lookup for a track just
// downloaded is usually a cache hit rather than a network call.
func resolveISRCFromSpotifyURL(spotifyURL string) string {
	parts := strings.Split(spotifyURL, "/")
	if len(parts) == 0 {
		return ""
	}
	spotifyID := strings.Split(parts[len(parts)-1], "?")[0]
	if spotifyID == "" {
		return ""
	}
	isrc, err := isrclookup.Shared().Resolve(spotifyID)
	if err != nil {
		return ""
	}
	return isrc
}
