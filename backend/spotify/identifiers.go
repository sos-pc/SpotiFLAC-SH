package spotify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
)

const (
	spotifyMetadataURLTemplate = "https://spclient.wg.spotify.com/metadata/4/%s/%s?market=from_token"
	spotifyBase62Alphabet      = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

type spotifyExternalID struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type spotifyTrackMetadata struct {
	ExternalID []spotifyExternalID `json:"external_id"`
}

// GetTrackISRC resolves a Spotify track's ISRC straight from Spotify's own
// internal catalog record (the metadata microservice), using the same
// anonymous session token as Query(). This is more authoritative than
// name-based cross-referencing through Song.link/Deezer, which can match the
// wrong edition/remaster of a track. Returns the ISRC uppercased and
// shape-validated (see parseTrackISRC).
//
// Only the ISRC is fetched. The album's UPC is available on the same service
// (one extra round-trip) but nothing consumes UPC yet, so it's deliberately
// left for when tag support for it actually lands.
func (c *SpotifyClient) GetTrackISRC(spotifyTrackID string) (string, error) {
	spotifyTrackID = strings.TrimSpace(spotifyTrackID)
	if spotifyTrackID == "" {
		return "", errors.New("spotify track ID is required")
	}

	gid, err := spotifyIDToGID(spotifyTrackID)
	if err != nil {
		return "", err
	}

	body, err := c.fetchMetadata("track", gid)
	if err != nil {
		return "", err
	}

	return parseTrackISRC(body)
}

// parseTrackISRC extracts and validates the ISRC from a raw track-metadata
// payload. Pure (no network) so it can be unit-tested against a fixture.
func parseTrackISRC(payload []byte) (string, error) {
	var track spotifyTrackMetadata
	if err := json.Unmarshal(payload, &track); err != nil {
		return "", fmt.Errorf("failed to decode Spotify track metadata: %w", err)
	}

	isrc := ""
	for _, id := range track.ExternalID {
		if strings.EqualFold(strings.TrimSpace(id.Type), "isrc") {
			if v := strings.TrimSpace(id.ID); v != "" {
				isrc = v
				break
			}
		}
	}
	if isrc == "" {
		return "", errors.New("no ISRC in Spotify track metadata")
	}

	isrc = strings.ToUpper(isrc)
	if !looksLikeISRC(isrc) {
		return "", fmt.Errorf("malformed ISRC in Spotify metadata: %q", isrc)
	}
	return isrc, nil
}

// looksLikeISRC does a light shape check (12 alphanumeric chars, e.g.
// USRC17607839) before we trust and cache a value — enough to reject an
// obviously corrupt/truncated response without pretending to validate the
// registrant/year segments.
func looksLikeISRC(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// fetchMetadata GETs an entity's raw metadata by GID, reusing the client's
// shared access token. The metadata microservice accepts the same anonymous
// bearer token as the GraphQL partner API used by Query(); on 401/403 the
// token is likely expired/insufficient, so we force one refresh and retry
// once — mirroring Query()'s reset-on-401 behavior.
func (c *SpotifyClient) fetchMetadata(entityType, gid string) ([]byte, error) {
	body, status, err := c.requestMetadata(entityType, gid)
	if err == nil {
		return body, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return nil, err
	}

	c.mu.Lock()
	c.accessToken = ""
	c.mu.Unlock()

	body, _, err = c.requestMetadata(entityType, gid)
	return body, err
}

func (c *SpotifyClient) requestMetadata(entityType, gid string) ([]byte, int, error) {
	// Token read/refresh under the lock, then release before the network
	// round-trip (same pattern as Query()) so concurrent callers sharing one
	// client don't serialize on every request.
	c.mu.Lock()
	if c.accessToken == "" {
		if err := c.getAccessToken(); err != nil {
			c.mu.Unlock()
			return nil, 0, err
		}
	}
	accessToken := c.accessToken
	c.mu.Unlock()

	req, err := http.NewRequest("GET", fmt.Sprintf(spotifyMetadataURLTemplate, entityType, gid), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("%w: metadata request failed: HTTP %d", SpotifyError, resp.StatusCode)
	}

	return body, resp.StatusCode, nil
}

// spotifyIDToGID converts a base62 Spotify entity ID (track/album) to the
// zero-padded 32-char hex GID the metadata microservice expects. Pure, so it
// can be unit-tested with deterministic inputs.
func spotifyIDToGID(entityID string) (string, error) {
	if entityID == "" {
		return "", errors.New("entity ID is empty")
	}

	value := big.NewInt(0)
	base := big.NewInt(62)

	for _, char := range entityID {
		index := strings.IndexRune(spotifyBase62Alphabet, char)
		if index < 0 {
			return "", fmt.Errorf("invalid base62 character: %q", string(char))
		}
		value.Mul(value, base)
		value.Add(value, big.NewInt(int64(index)))
	}

	hex := value.Text(16)
	if len(hex) < 32 {
		hex = strings.Repeat("0", 32-len(hex)) + hex
	}
	return hex, nil
}
