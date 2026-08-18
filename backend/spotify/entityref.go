package spotify

import (
	"regexp"
	"strings"
)

// entityRef matches a Spotify reference in a URL. The optional segment before
// the type is the locale Spotify puts in shared links —
// open.spotify.com/intl-fr/playlist/… — which the anchored form of this pattern
// silently returned nothing for. Query parameters need no handling: the ID is
// matched by its own character class, so `?si=…` simply falls outside it.
// (?i) is safe on the whole pattern: the ID class already accepts both cases,
// so the only thing it loosens is the entity type. Worth loosening — this
// decides both "is this an album" and "are these the same entity", and a link
// that arrived with a capitalised segment is still the same playlist.
var entityRef = regexp.MustCompile(`(?i)spotify\.com/(?:[a-zA-Z0-9-]+/)?(track|album|playlist|artist)/([a-zA-Z0-9]+)`)

// ParseEntityRef extracts the entity kind ("track", "album", "playlist",
// "artist") and its ID from any Spotify reference: a `spotify:…` URI, a plain
// URL, or a localised one, with or without query parameters. Both values are
// empty when the reference is not recognised — callers must treat that as
// "unknown", never as a match.
// The kind is returned lowercased so callers can compare it directly; the ID
// never is, because Spotify IDs are base62 and case carries meaning.
func ParseEntityRef(ref string) (kind, id string) {
	if strings.HasPrefix(strings.ToLower(ref), "spotify:") {
		parts := strings.Split(ref, ":")
		if len(parts) >= 3 {
			return strings.ToLower(parts[1]), parts[2]
		}
	}
	if m := entityRef.FindStringSubmatch(ref); len(m) == 3 {
		return strings.ToLower(m[1]), m[2]
	}
	return "", ""
}
