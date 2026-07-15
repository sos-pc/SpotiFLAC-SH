package providerutil

import (
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
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
// spotifyURL via their own Songlink call instead of reusing one already
// known by the caller, which both duplicated the lookup logic and spent an
// extra call against Songlink's shared, rate-limited client for a result
// the caller may already have.
//
// If isrc is already known (e.g. Qobuz resolves it earlier via its own
// ISRC-based search and has no need to re-derive it), pass it directly and
// spotifyURL can be left empty. Otherwise pass spotifyURL and isrc empty;
// the Spotify track ID is extracted from it and resolved to an ISRC via
// Songlink.
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
			res.Metadata.Genre = got.Genre
			res.Source = got.Source
			res.Outcome = got.Outcome
		}
		ch <- res
	}, func() { ch <- MBResult{} })

	return ch
}

// resolveISRCFromSpotifyURL extracts the Spotify track ID from a Spotify
// track URL (the last path segment, query string stripped) and resolves
// its ISRC via the shared Songlink client. Returns "" if the URL can't be
// parsed or Songlink has no ISRC for it.
func resolveISRCFromSpotifyURL(spotifyURL string) string {
	parts := strings.Split(spotifyURL, "/")
	if len(parts) == 0 {
		return ""
	}
	spotifyID := strings.Split(parts[len(parts)-1], "?")[0]
	if spotifyID == "" {
		return ""
	}
	client := songlink.GetSongLinkClient()
	isrc, err := client.GetISRC(spotifyID)
	if err != nil {
		return ""
	}
	return isrc
}
