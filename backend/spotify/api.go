package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

func GetSpotifyDataWithAPI(ctx context.Context, spotifyURL string, useAPI bool, apiBaseURL string, batch bool, delay time.Duration) (interface{}, error) {
	if !useAPI || apiBaseURL == "" {

		return GetFilteredSpotifyData(ctx, spotifyURL, batch, delay)
	}

	spotifyType, id := parseSpotifyURLToTypeAndID(spotifyURL)
	if spotifyType == "" || id == "" {
		return nil, fmt.Errorf("invalid Spotify URL: %s", spotifyURL)
	}

	apiURL := fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(apiBaseURL, "/"), spotifyType, id)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create API request: %w", err)
	}

	client := util.NewHTTPClient(30 * time.Second)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SpotFetch API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SpotFetch API error: HTTP %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read API response: %w", err)
	}

	var data interface{}

	switch spotifyType {
	case "track":
		var trackResp TrackResponse
		if err := json.Unmarshal(bodyBytes, &trackResp); err != nil {
			return nil, fmt.Errorf("failed to decode track response: %w", err)
		}
		data = trackResp
	case "album":
		var albumResp AlbumResponsePayload
		if err := json.Unmarshal(bodyBytes, &albumResp); err != nil {
			return nil, fmt.Errorf("failed to decode album response: %w", err)
		}
		data = &albumResp
	case "playlist":
		var playlistResp PlaylistResponsePayload
		if err := json.Unmarshal(bodyBytes, &playlistResp); err != nil {
			return nil, fmt.Errorf("failed to decode playlist response: %w", err)
		}
		data = playlistResp
	case "artist":
		var artistResp ArtistDiscographyPayload
		if err := json.Unmarshal(bodyBytes, &artistResp); err != nil {
			return nil, fmt.Errorf("failed to decode artist response: %w", err)
		}
		data = &artistResp
	default:
		return nil, fmt.Errorf("unsupported Spotify type: %s", spotifyType)
	}

	return data, nil
}

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

func parseSpotifyURLToTypeAndID(url string) (string, string) {
	return ParseEntityRef(url)
}
