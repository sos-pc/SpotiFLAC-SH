package spotify

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"sort"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

var SpotifyError = errors.New("spotify error")

// SpotifyClient is shared across concurrent goroutines when callers batch
// several requests against one initialized session (see
// formatArtistDiscographyData/StreamArtistDiscography in metadata.go, which
// fan out up to 5 concurrent album fetches against a single client to avoid
// each one paying for its own session handshake). mu guards every mutable
// field below — without it, two goroutines racing to reset/reinitialize an
// expired token concurrently write the same cookies map at the same time,
// which is a fatal, unrecoverable "concurrent map writes" runtime error in
// Go (not a panic recover() can catch). Reads/writes of these fields must
// only ever happen while holding mu; the *http.Client itself needs no
// protection (net/http clients are already safe for concurrent use).
type SpotifyClient struct {
	client *http.Client

	mu            sync.Mutex
	accessToken   string
	clientToken   string
	clientID      string
	deviceID      string
	clientVersion string
	cookies       map[string]string
}

// spotifyTokenCache shares one access token across every SpotifyClient in the
// process. The token was a per-instance field, and NewSpotifyClient() is called
// from 9 distinct sites (measured 2026-07-18), so a single fetch or download
// could redo several signed TOTP handshakes against Spotify — pure extra
// exposure to rate limiting for a credential that is identical every time.
//
// Invalidated by invalidateSpotifyTokenCache() whenever a caller sees a 401,
// otherwise a revoked token would keep being served from here.
var spotifyTokenCache struct {
	sync.Mutex
	accessToken string
	clientID    string
	expiresAt   time.Time
}

// Refresh a little before the real expiry rather than exactly on it, so a
// request already in flight cannot land just past the boundary.
const spotifyTokenCacheSkew = 60 * time.Second

func spotifyTokenCacheValidLocked(now time.Time) bool {
	return spotifyTokenCache.accessToken != "" &&
		spotifyTokenCache.clientID != "" &&
		now.Before(spotifyTokenCache.expiresAt.Add(-spotifyTokenCacheSkew))
}

func invalidateSpotifyTokenCache() {
	spotifyTokenCache.Lock()
	defer spotifyTokenCache.Unlock()
	spotifyTokenCache.accessToken = ""
	spotifyTokenCache.clientID = ""
	spotifyTokenCache.expiresAt = time.Time{}
}

func NewSpotifyClient() *SpotifyClient {
	return &SpotifyClient{
		client:  util.NewHTTPClient(30 * time.Second),
		cookies: make(map[string]string),
	}
}

// generateTOTPAt builds the code for a GIVEN instant rather than always for
// time.Now(). Spotify validates the code against its own clock, so a host whose
// clock has drifted produces a code that is silently wrong — retrying with the
// same instant just repeats the same wrong code. getAccessToken uses this to
// walk the adjacent 30s windows (upstream-catchup.md §S8).
func (c *SpotifyClient) generateTOTPAt(at time.Time) (string, int, error) {

	secret := "GM3TMMJTGYZTQNZVGM4DINJZHA4TGOBYGMZTCMRTGEYDSMJRHE4TEOBUG4YTCMRUGQ4DQOJUGQYTAMRRGA2TCMJSHE3TCMBY"
	version := 61

	key, err := otp.NewKeyFromURL(fmt.Sprintf("otpauth://totp/secret?secret=%s", secret))
	if err != nil {
		return "", 0, err
	}

	totpCode, err := totp.GenerateCode(key.Secret(), at)
	if err != nil {
		return "", 0, err
	}

	return totpCode, version, nil
}

// getAccessToken, getSessionInfo and getClientToken all mutate the shared
// token/cookie fields and must only be called while c.mu is held — they're
// only ever reached via initializeLocked (directly, or transitively through
// getClientToken's own fallback call to the first two).
func (c *SpotifyClient) getAccessToken() error {
	spotifyTokenCache.Lock()
	defer spotifyTokenCache.Unlock()

	// A token fetched by any other client in this process is just as valid.
	if spotifyTokenCacheValidLocked(time.Now()) {
		c.accessToken = spotifyTokenCache.accessToken
		c.clientID = spotifyTokenCache.clientID
		return nil
	}

	// One attempt per TOTP window: the current one, then the two adjacent ones.
	// Retrying with time.Now() alone (what this used to do) only helps when the
	// delay happens to cross a rotation boundary — it cannot recover from a
	// host clock that is simply off, which produces a wrong code every time.
	totpWindows := []time.Duration{0, -30 * time.Second, 30 * time.Second}
	maxAttempts := len(totpWindows)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		totpCode, version, err := c.generateTOTPAt(time.Now().Add(totpWindows[attempt]))
		if err != nil {
			return err
		}

		req, err := http.NewRequest("GET", "https://open.spotify.com/api/token", nil)
		if err != nil {
			return err
		}

		q := req.URL.Query()
		q.Add("reason", "init")
		q.Add("productType", "web-player")
		q.Add("totp", totpCode)
		q.Add("totpVer", strconv.Itoa(version))
		q.Add("totpServer", totpCode)
		req.URL.RawQuery = q.Encode()

		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")
		req.Header.Set("Content-Type", "application/json;charset=UTF-8")

		resp, err := c.client.Do(req)
		if err != nil {
			// Network error — retry after a short delay
			if attempt < maxAttempts-1 {
				time.Sleep(2 * time.Second)
				continue
			}
			return err
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}

		if resp.StatusCode >= 500 && attempt < maxAttempts-1 {
			// Transient server error — wait and retry
			time.Sleep(time.Duration(3*(attempt+1)) * time.Second)
			continue
		}
		if resp.StatusCode != 200 {
			// 4xx or exhausted retries — fail immediately
			return fmt.Errorf("%w: access token request failed: HTTP %d", SpotifyError, resp.StatusCode)
		}

		var data map[string]interface{}
		if err := json.Unmarshal(body, &data); err != nil {
			return err
		}

		c.accessToken = getString(data, "accessToken")
		c.clientID = getString(data, "clientId")

		// Share it with every other client in the process. Spotify returns the
		// real expiry; fall back to a conservative 55 min when it is absent.
		expiresAt := time.Now().Add(55 * time.Minute)
		if ms := getFloat64(data, "accessTokenExpirationTimestampMs"); ms > 0 {
			expiresAt = time.UnixMilli(int64(ms))
		}
		spotifyTokenCache.accessToken = c.accessToken
		spotifyTokenCache.clientID = c.clientID
		spotifyTokenCache.expiresAt = expiresAt

		for _, cookie := range resp.Cookies() {
			if cookie.Name == "sp_t" {
				c.deviceID = cookie.Value
			}
			c.cookies[cookie.Name] = cookie.Value
		}

		return nil
	}
	return fmt.Errorf("%w: access token request failed after %d attempts", SpotifyError, maxAttempts)
}

func (c *SpotifyClient) getSessionInfo() error {
	req, err := http.NewRequest("GET", "https://open.spotify.com", nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	for name, value := range c.cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: session initialization failed: HTTP %d", SpotifyError, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`<script id="appServerConfig" type="text/plain">([^<]+)</script>`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) > 1 {
		decoded, err := base64.StdEncoding.DecodeString(matches[1])
		if err == nil {
			var cfg map[string]interface{}
			if json.Unmarshal(decoded, &cfg) == nil {
				c.clientVersion = getString(cfg, "clientVersion")
			}
		}
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == "sp_t" {
			c.deviceID = cookie.Value
		}
		c.cookies[cookie.Name] = cookie.Value
	}

	return nil
}

func (c *SpotifyClient) getClientToken() error {
	if c.clientID == "" || c.deviceID == "" || c.clientVersion == "" {
		if err := c.getSessionInfo(); err != nil {
			return err
		}
		if err := c.getAccessToken(); err != nil {
			return err
		}
	}

	payload := map[string]interface{}{
		"client_data": map[string]interface{}{
			"client_version": c.clientVersion,
			"client_id":      c.clientID,
			"js_sdk_data": map[string]interface{}{
				"device_brand": "unknown",
				"device_model": "unknown",
				"os":           "windows",
				"os_version":   "NT 10.0",
				"device_id":    c.deviceID,
				"device_type":  "computer",
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", "https://clienttoken.spotify.com/v1/clienttoken", bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Authority", "clienttoken.spotify.com")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: client token request failed: HTTP %d", SpotifyError, resp.StatusCode)
	}

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}

	if getString(data, "response_type") != "RESPONSE_GRANTED_TOKEN_RESPONSE" {
		return fmt.Errorf("%w: invalid client token response type", SpotifyError)
	}

	grantedToken := getMap(data, "granted_token")
	c.clientToken = getString(grantedToken, "token")

	return nil
}

// Initialize runs the full session/token handshake. Safe to call on a
// client that's about to be shared across goroutines (see the doc comment
// on SpotifyClient), as long as no concurrent Query() calls are already in
// flight when it's called — the normal usage pattern (Initialize once,
// then fan out concurrent Query calls) satisfies this.
func (c *SpotifyClient) Initialize() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initializeLocked()
}

// initializeLocked is Initialize's body, factored out so Query can reuse it
// without deadlocking on c.mu (Go's sync.Mutex isn't reentrant). Callers
// must hold c.mu.
func (c *SpotifyClient) initializeLocked() error {
	if err := c.getSessionInfo(); err != nil {
		return err
	}
	if err := c.getAccessToken(); err != nil {
		return err
	}
	return c.getClientToken()
}

func (c *SpotifyClient) Query(payload map[string]interface{}) (map[string]interface{}, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Token read/refresh is the only state a shared client mutates
		// concurrently — keep that under the lock, but copy the values out
		// and release before the network round-trip below so multiple
		// Query calls sharing one client still run in parallel instead of
		// serializing on every request.
		c.mu.Lock()
		if c.accessToken == "" || c.clientToken == "" {
			if err := c.initializeLocked(); err != nil {
				c.mu.Unlock()
				return nil, err
			}
		}
		accessToken, clientToken, clientVersion := c.accessToken, c.clientToken, c.clientVersion
		c.mu.Unlock()

		req, err := http.NewRequest("POST", "https://api-partner.spotify.com/pathfinder/v2/query", bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}

		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Client-Token", clientToken)
		req.Header.Set("Spotify-App-Version", clientVersion)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36")

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		switch resp.StatusCode {
		case 200:
			var result map[string]interface{}
			if err := json.Unmarshal(body, &result); err != nil {
				return nil, err
			}
			return result, nil
		case 401:
			// Token expired — reset and retry with fresh tokens. The shared
			// cache must be cleared too, otherwise the refresh below would read
			// the very token Spotify just rejected straight back out of it.
			c.mu.Lock()
			c.accessToken = ""
			c.clientToken = ""
			c.mu.Unlock()
			invalidateSpotifyTokenCache()
			continue
		case 429:
			// Rate limited — honour Retry-After if present.
			// Fallback: exponential backoff 10s / 30s / 60s.
			backoffs := []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}
			wait := backoffs[attempt]
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if n, err := strconv.Atoi(ra); err == nil && n > 0 {
					wait = time.Duration(n) * time.Second
				}
			}
			fmt.Printf("⚠ Spotify API rate limited (429), retrying in %v (attempt %d/%d)...\n", wait, attempt+1, maxAttempts)
			time.Sleep(wait)
			continue
		default:
			errorText := string(body)
			if len(errorText) > 200 {
				errorText = errorText[:200]
			}
			return nil, fmt.Errorf("%w: API query failed: HTTP %d | %s", SpotifyError, resp.StatusCode, errorText)
		}
	}
	return nil, fmt.Errorf("%w: API query failed after %d attempts", SpotifyError, maxAttempts)
}

func getString(m map[string]interface{}, key string) string {
	if val, ok := m[key].(string); ok {
		return val
	}
	return ""
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	if val, ok := m[key].(map[string]interface{}); ok {
		return val
	}
	return make(map[string]interface{})
}

func getSlice(m map[string]interface{}, key string) []interface{} {
	if val, ok := m[key].([]interface{}); ok {
		return val
	}
	return nil
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if val, ok := m[key].(float64); ok {
		return val
	}
	return 0
}

func getBool(m map[string]interface{}, key string) bool {
	if val, ok := m[key].(bool); ok {
		return val
	}
	return false
}

func extractArtists(artistsData map[string]interface{}) []map[string]interface{} {
	items := getSlice(artistsData, "items")

	artists := []map[string]interface{}{}
	for _, item := range items {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		profile := getMap(itemMap, "profile")
		artistInfo := map[string]interface{}{
			"name": getString(profile, "name"),
		}
		artists = append(artists, artistInfo)
	}
	return artists
}

func extractCoverImage(coverData map[string]interface{}) map[string]interface{} {
	if len(coverData) == 0 {
		return nil
	}

	var sources []interface{}
	if srcs, ok := coverData["sources"].([]interface{}); ok {
		sources = srcs
	} else if squareImg, ok := coverData["squareCoverImage"].(map[string]interface{}); ok {
		if img, ok := squareImg["image"].(map[string]interface{}); ok {
			if data, ok := img["data"].(map[string]interface{}); ok {
				if srcs, ok := data["sources"].([]interface{}); ok {
					sources = srcs
				}
			}
		}
	}

	if len(sources) == 0 {
		return nil
	}

	type sourceInfo struct {
		url    string
		width  float64
		height float64
	}

	filteredSources := []sourceInfo{}
	for _, s := range sources {
		sMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		url := getString(sMap, "url")
		if url == "" {
			continue
		}

		width := getFloat64(sMap, "width")
		if width == 0 {
			width = getFloat64(sMap, "maxWidth")
		}
		height := getFloat64(sMap, "height")
		if height == 0 {
			height = getFloat64(sMap, "maxHeight")
		}

		if (width > 64 && height > 64) || (width == 0 && height == 0 && url != "") {
			filteredSources = append(filteredSources, sourceInfo{url: url, width: width, height: height})
		}
	}

	if len(filteredSources) == 0 {
		return nil
	}

	sort.Slice(filteredSources, func(i, j int) bool {
		return filteredSources[i].width < filteredSources[j].width
	})

	var smallURL, mediumURL, imageID, fallbackURL string

	for _, source := range filteredSources {
		if source.width == 300 {
			smallURL = source.url
		} else if source.width == 640 {
			mediumURL = source.url
		} else if source.width == 0 {
			fallbackURL = source.url
		}

		if imageID == "" && source.url != "" {
			if strings.Contains(source.url, "ab67616d0000b273") {
				parts := strings.Split(source.url, "ab67616d0000b273")
				if len(parts) > 1 {
					imageID = parts[len(parts)-1]
				}
			} else if strings.Contains(source.url, "ab67616d00001e02") {
				parts := strings.Split(source.url, "ab67616d00001e02")
				if len(parts) > 1 {
					imageID = parts[len(parts)-1]
				}
			} else if strings.Contains(source.url, "/image/") {
				parts := strings.Split(source.url, "/image/")
				if len(parts) > 1 {
					imagePart := strings.Split(parts[len(parts)-1], "?")[0]
					if len(imagePart) > 20 {
						prefixes := []string{"ab67616d0000b273", "ab67616d00001e02", "ab67616d00004851"}
						for _, prefix := range prefixes {
							if strings.Contains(imagePart, prefix) {
								subParts := strings.Split(imagePart, prefix)
								if len(subParts) > 1 {
									imageID = subParts[len(subParts)-1]
									break
								}
							}
						}
					}
				}
			}
		}
	}

	largeURL := ""
	if imageID != "" {
		largeURL = "https://i.scdn.co/image/ab67616d000082c1" + imageID
	}

	result := map[string]interface{}{}
	if smallURL != "" {
		result["small"] = smallURL
	}
	if mediumURL != "" {
		result["medium"] = mediumURL
	}
	if largeURL != "" {
		result["large"] = largeURL
	}

	if len(result) == 0 && fallbackURL != "" {
		result["small"] = fallbackURL
		result["medium"] = fallbackURL
		result["large"] = fallbackURL
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

func extractDuration(ms float64) map[string]interface{} {
	totalSeconds := int(ms) / 1000
	minutes := totalSeconds / 60
	seconds := totalSeconds % 60
	return map[string]interface{}{
		"formatted": fmt.Sprintf("%d:%02d", minutes, seconds),
	}
}

// FilterTrack builds the typed track contract directly from the raw trackUnion
// response (R2). The optional albumFetchData (a reshaped second album fetch, see
// fetchTrack) refines disc/total-disc numbers. Decomposed into focused helpers;
// input parsing stays on the tolerant getMap/getString helpers because the raw
// GraphQL is optional-heavy and has polymorphic fields (e.g. album "artists" is
// read as both an object and a string).
func FilterTrack(data map[string]interface{}, albumFetchData ...map[string]interface{}) apiTrackResponse {
	trackData := getMap(getMap(data, "data"), "trackUnion")
	if len(trackData) == 0 {
		return apiTrackResponse{}
	}
	var albumFetchDataMap map[string]interface{}
	if len(albumFetchData) > 0 {
		albumFetchDataMap = albumFetchData[0]
	}

	albumData := getMap(trackData, "albumOfTrack")

	var result apiTrackResponse
	result.ID = getString(trackData, "id")
	result.Name = getString(trackData, "name")
	result.Artists = strings.Join(resolveTrackArtistNames(trackData), ", ")
	result.Track = int(getFloat64(trackData, "trackNumber"))
	result.Plays = getString(trackData, "playcount")
	result.IsExplicit = getString(getMap(trackData, "contentRating"), "label") == "EXPLICIT"
	result.Duration = getString(extractDuration(getFloat64(getMap(trackData, "duration"), "totalMilliseconds")), "formatted")

	coverObj := extractCoverImage(getMap(trackData, "visualIdentity"))
	if coverObj == nil && len(albumData) > 0 {
		coverObj = extractCoverImage(getMap(albumData, "coverArt"))
	}
	if coverObj != nil {
		result.Cover.Small = getString(coverObj, "small")
		result.Cover.Medium = getString(coverObj, "medium")
		result.Cover.Large = getString(coverObj, "large")
	}

	// Highest disc number across the album's own track list — the fallback for
	// total discs when albumFetchData carries none.
	albumTracksMaxDisc := 0
	if len(albumData) > 0 {
		result.Copyright = joinAlbumCopyright(albumData)
		albumTracksMaxDisc = maxDiscAcrossAlbumTracks(albumData)

		releaseDate, releaseYear := parseTrackReleaseDate(getMap(albumData, "date"))
		albumArtists, albumLabel := resolveAlbumArtistsAndLabel(albumData, albumFetchDataMap)

		albumID := getString(albumData, "id")
		if albumID == "" {
			if albumURI := getString(albumData, "uri"); strings.Contains(albumURI, ":") {
				parts := strings.Split(albumURI, ":")
				albumID = parts[len(parts)-1]
			}
		}

		result.Album.ID = albumID
		result.Album.Name = getString(albumData, "name")
		result.Album.Released = releaseDate
		result.Album.Year = releaseYear
		result.Album.Tracks = int(getFloat64(getMap(albumData, "tracks"), "totalCount"))
		result.Album.Artists = albumArtists
		result.Album.Label = albumLabel
	}

	discNumber := int(getFloat64(trackData, "discNumber"))
	if discNumber == 0 {
		discNumber = 1
	}
	totalDiscsFromAlbum := 0
	maxDiscFromAlbum := 0
	if albumFetchDataMap != nil {
		albumUnion := getMap(getMap(albumFetchDataMap, "data"), "albumUnion")
		if len(albumUnion) > 0 {
			if discsData := getMap(albumUnion, "discs"); len(discsData) > 0 {
				totalDiscsFromAlbum = int(getFloat64(discsData, "totalCount"))
			}
			currentTrackID := getString(trackData, "id")
			for _, item := range getSlice(getMap(albumUnion, "tracks"), "items") {
				itemMap, ok := item.(map[string]interface{})
				if !ok {
					continue
				}
				trackItem := getMap(itemMap, "track")
				if len(trackItem) == 0 {
					continue
				}
				dNum := int(getFloat64(trackItem, "discNumber"))
				if dNum > maxDiscFromAlbum {
					maxDiscFromAlbum = dNum
				}
				trackURI := getString(trackItem, "uri")
				if strings.Contains(trackURI, currentTrackID) || getString(trackItem, "id") == currentTrackID {
					if dNum > 0 {
						discNumber = dNum
					}
				}
			}
		}
	}

	totalDiscs := 1
	if totalDiscsFromAlbum > 0 {
		totalDiscs = totalDiscsFromAlbum
	} else if maxDiscFromAlbum > 0 {
		totalDiscs = maxDiscFromAlbum
	} else if albumTracksMaxDisc > 0 {
		totalDiscs = albumTracksMaxDisc
	}

	result.Disc = discNumber
	result.Discs = totalDiscs
	return result
}

// resolveTrackArtistNames returns the track's artist names, trying trackUnion
// .artists first, then the firstArtist/otherArtists profile lists, then
// albumOfTrack.artists — the same three-way cascade the pre-R2 code used.
func resolveTrackArtistNames(trackData map[string]interface{}) []string {
	artists := extractArtists(getMap(trackData, "artists"))

	if len(artists) == 0 {
		artists = []map[string]interface{}{}
		for _, item := range getSlice(getMap(trackData, "firstArtist"), "items") {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if profileMap, ok := itemMap["profile"].(map[string]interface{}); ok {
				artists = append(artists, map[string]interface{}{"name": getString(profileMap, "name")})
			}
		}
		for _, item := range getSlice(getMap(trackData, "otherArtists"), "items") {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			if profileMap, ok := itemMap["profile"].(map[string]interface{}); ok {
				artists = append(artists, map[string]interface{}{"name": getString(profileMap, "name")})
			}
		}
	}

	if len(artists) == 0 {
		if albumData := getMap(trackData, "albumOfTrack"); len(albumData) > 0 {
			artists = extractArtists(getMap(albumData, "artists"))
		}
	}

	names := make([]string, 0, len(artists))
	for _, a := range artists {
		names = append(names, getString(a, "name"))
	}
	return names
}

// joinAlbumCopyright joins the album's copyright texts (comma-separated),
// preferring "C" (copyright) entries and falling back to "P" (phonogram
// right) entries when no "C" entry exists.
//
// Verified on a real release (Karma To Burn — "19", Alamo Records/Sony):
// Spotify returned exactly one entry, type "P", no "C" at all. The previous
// version of this function unconditionally dropped every "P" entry, so an
// album with only a phonogram notice — common for smaller/independent
// releases, where a distributor lists "℗ 2019 Label" and nothing else —
// silently produced an empty Copyright, even though Spotify had supplied the
// data. That emptiness then fed straight into the retag pass's selection
// clause (t.copyright = ''), which is why it was the single largest cause of
// tracks never converging: measured on a real library, it accounted for 212
// of 234 tracks stuck re-selecting on every pass, against 24 for genre.
func joinAlbumCopyright(albumData map[string]interface{}) string {
	var preferred, phonogram []string
	copyrightData := getMap(albumData, "copyright")
	if len(copyrightData) > 0 {
		for _, item := range getSlice(copyrightData, "items") {
			itemMap, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			text := getString(itemMap, "text")
			if text == "" {
				continue
			}
			if getString(itemMap, "type") == "P" {
				phonogram = append(phonogram, text)
			} else {
				preferred = append(preferred, text)
			}
		}
	}
	if len(preferred) > 0 {
		return strings.Join(preferred, ", ")
	}
	return strings.Join(phonogram, ", ")
}

// maxDiscAcrossAlbumTracks returns the highest disc number seen in the album's
// own track list (0 if none — treated as "unknown" by the caller).
func maxDiscAcrossAlbumTracks(albumData map[string]interface{}) int {
	tracksData := getMap(albumData, "tracks")
	if len(tracksData) == 0 {
		return 0
	}
	discNumbers := map[int]bool{}
	for _, item := range getSlice(tracksData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		trackItem := getMap(itemMap, "track")
		if len(trackItem) == 0 {
			continue
		}
		discNum := int(getFloat64(trackItem, "discNumber"))
		if discNum == 0 {
			discNum = 1
		}
		discNumbers[discNum] = true
	}
	if len(discNumbers) == 0 {
		return 0
	}
	maxDisc := 1
	for discNum := range discNumbers {
		if discNum > maxDisc {
			maxDisc = discNum
		}
	}
	return maxDisc
}

// parseTrackReleaseDate returns the album release date (YYYY-MM-DD or YYYY) and
// year from a raw date node, handling both the isoString form and the
// year/month/day form.
func parseTrackReleaseDate(dateInfo map[string]interface{}) (string, int) {
	releaseDate := getString(dateInfo, "isoString")
	releaseYear := 0
	if releaseDate == "" && len(dateInfo) > 0 {
		yearStr := getString(dateInfo, "year")
		monthStr := getString(dateInfo, "month")
		dayStr := getString(dateInfo, "day")
		if yearStr != "" {
			if year, err := strconv.Atoi(yearStr); err == nil {
				releaseYear = year
				if monthStr != "" && dayStr != "" {
					month, _ := strconv.Atoi(monthStr)
					day, _ := strconv.Atoi(dayStr)
					releaseDate = fmt.Sprintf("%s-%02d-%02d", yearStr, month, day)
				} else {
					releaseDate = yearStr
				}
			}
		}
	} else if releaseDate != "" {
		parts := strings.Split(releaseDate, "T")
		if len(parts) > 0 {
			releaseDate = parts[0]
		} else {
			parts = strings.Split(releaseDate, " ")
			if len(parts) > 0 {
				releaseDate = parts[0]
			}
		}
		dateParts := strings.Split(releaseDate, "-")
		if len(dateParts) > 0 && dateParts[0] != "" {
			if year, err := strconv.Atoi(dateParts[0]); err == nil {
				releaseYear = year
			}
		}
	}
	return releaseDate, releaseYear
}

// resolveAlbumArtistsAndLabel derives the album-artist string and label,
// preferring the reshaped albumFetchData (which may store "artists" as a
// pre-joined string) and falling back to albumOfTrack.artists.
func resolveAlbumArtistsAndLabel(albumData, albumFetchDataMap map[string]interface{}) (string, string) {
	albumArtistsString := ""
	albumLabel := ""
	if len(albumFetchDataMap) > 0 {
		albumUnionData := getMap(getMap(albumFetchDataMap, "data"), "albumUnion")
		if len(albumUnionData) > 0 {
			if names := artistNames(extractArtists(getMap(albumUnionData, "artists"))); len(names) > 0 {
				albumArtistsString = strings.Join(names, ", ")
			}
			if albumArtistsString == "" {
				albumArtistsString = getString(albumUnionData, "artists")
			}
			albumLabel = getString(albumUnionData, "label")
		}
	}
	if albumArtistsString == "" {
		if names := artistNames(extractArtists(getMap(albumData, "artists"))); len(names) > 0 {
			albumArtistsString = strings.Join(names, ", ")
		}
	}
	return albumArtistsString, albumLabel
}

// artistNames maps extractArtists' output to a plain name slice.
func artistNames(artists []map[string]interface{}) []string {
	names := make([]string, 0, len(artists))
	for _, a := range artists {
		names = append(names, getString(a, "name"))
	}
	return names
}

// FilterAlbum builds the typed album contract directly from the raw albumUnion
// response (R2). Input is still navigated with the tolerant getMap/getString
// helpers — the raw Spotify GraphQL is optional-heavy and has polymorphic
// fields — but the output is the typed apiAlbumResponse, so there is no longer
// a lossy map->json->struct bridge in metadata.go.
func FilterAlbum(data map[string]interface{}) apiAlbumResponse {
	albumData := getMap(getMap(data, "data"), "albumUnion")
	if len(albumData) == 0 {
		return apiAlbumResponse{}
	}

	albumArtists := extractArtists(getMap(albumData, "artists"))
	artistNames := make([]string, 0, len(albumArtists))
	for _, artist := range albumArtists {
		artistNames = append(artistNames, getString(artist, "name"))
	}

	cover := ""
	if coverObj := extractCoverImage(getMap(albumData, "coverArt")); coverObj != nil {
		cover = getString(coverObj, "small")
		if cover == "" {
			cover = getString(coverObj, "medium")
		}
		if cover == "" {
			cover = getString(coverObj, "large")
		}
	}

	tracks := []apiAlbumTrack{}
	for _, item := range getSlice(getMap(albumData, "tracksV2"), "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		track := getMap(itemMap, "track")
		if len(track) == 0 {
			continue
		}
		tracks = append(tracks, buildAlbumTrack(track))
	}

	releaseDate := getString(getMap(albumData, "date"), "isoString")
	if releaseDate != "" && strings.Contains(releaseDate, "T") {
		releaseDate = strings.Split(releaseDate, "T")[0]
	}

	albumID := ""
	if albumURI := getString(albumData, "uri"); strings.Contains(albumURI, ":") {
		parts := strings.Split(albumURI, ":")
		albumID = parts[len(parts)-1]
	}

	totalDiscs := 1
	if discsData := getMap(albumData, "discs"); len(discsData) > 0 {
		totalDiscs = int(getFloat64(discsData, "totalCount"))
	}

	result := apiAlbumResponse{
		ID:          albumID,
		Name:        getString(albumData, "name"),
		Artists:     strings.Join(artistNames, ", "),
		Cover:       cover,
		ReleaseDate: releaseDate,
		Count:       len(tracks),
		Label:       getString(albumData, "label"),
		Tracks:      tracks,
	}
	result.Discs.TotalCount = totalDiscs
	return result
}

// buildAlbumTrack maps one raw albumUnion.tracksV2 track node to apiAlbumTrack.
func buildAlbumTrack(track map[string]interface{}) apiAlbumTrack {
	artistsData := getMap(track, "artists")

	trackArtistNames := []string{}
	for _, artist := range extractArtists(artistsData) {
		trackArtistNames = append(trackArtistNames, getString(artist, "name"))
	}

	artistIDs := []string{}
	for _, artistItem := range getSlice(artistsData, "items") {
		artistItemMap, ok := artistItem.(map[string]interface{})
		if !ok {
			continue
		}
		artistURI := getString(artistItemMap, "uri")
		if artistURI != "" && strings.Contains(artistURI, ":") {
			parts := strings.Split(artistURI, ":")
			if artistID := parts[len(parts)-1]; artistID != "" {
				artistIDs = append(artistIDs, artistID)
			}
		}
	}

	trackID := ""
	if trackURI := getString(track, "uri"); strings.Contains(trackURI, ":") {
		parts := strings.Split(trackURI, ":")
		trackID = parts[len(parts)-1]
	}

	discNumber := int(getFloat64(track, "discNumber"))
	if discNumber == 0 {
		discNumber = 1
	}

	return apiAlbumTrack{
		ID:         trackID,
		Name:       getString(track, "name"),
		Artists:    strings.Join(trackArtistNames, ", "),
		ArtistIds:  artistIDs,
		Duration:   getString(extractDuration(getFloat64(getMap(track, "duration"), "totalMilliseconds")), "formatted"),
		Plays:      getString(track, "playcount"),
		IsExplicit: getString(getMap(track, "contentRating"), "label") == "EXPLICIT",
		DiscNumber: discNumber,
	}
}

// FilterPlaylist builds the typed playlist contract directly from the raw
// playlistV2 response (R2). Owner, cover and per-track construction are split
// into helpers; input parsing stays on the tolerant helpers.
func FilterPlaylist(data map[string]interface{}) apiPlaylistResponse {
	playlistData := getMap(getMap(data, "data"), "playlistV2")
	if len(playlistData) == 0 {
		return apiPlaylistResponse{}
	}

	var result apiPlaylistResponse
	result.Name = getString(playlistData, "name")
	result.Description = getString(playlistData, "description")
	result.Owner.Name, result.Owner.Avatar = resolvePlaylistOwner(playlistData)
	result.Cover = resolvePlaylistCover(playlistData)

	tracks := []apiPlaylistTrack{}
	content := getMap(playlistData, "content")
	for _, item := range getSlice(content, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if track, ok := buildPlaylistTrack(itemMap); ok {
			tracks = append(tracks, track)
		}
	}
	result.Tracks = tracks

	if playlistURI := getString(playlistData, "uri"); strings.Contains(playlistURI, ":") {
		parts := strings.Split(playlistURI, ":")
		result.ID = parts[len(parts)-1]
	}

	count := len(tracks)
	if totalCount := getFloat64(content, "totalCount"); totalCount > 0 {
		count = int(totalCount)
	}
	result.Count = count
	result.Followers = playlistFollowers(playlistData)

	return result
}

// resolvePlaylistOwner returns the playlist owner's name and avatar URL,
// preferring the 300px-wide avatar source and otherwise the first source.
func resolvePlaylistOwner(playlistData map[string]interface{}) (name, avatar string) {
	ownerData := getMap(getMap(playlistData, "ownerV2"), "data")
	if len(ownerData) == 0 {
		return "", ""
	}
	name = getString(ownerData, "name")
	if avatarData := getMap(ownerData, "avatar"); len(avatarData) > 0 {
		sources := getSlice(avatarData, "sources")
		found := false
		for _, source := range sources {
			sourceMap, ok := source.(map[string]interface{})
			if !ok {
				continue
			}
			if getFloat64(sourceMap, "width") == 300 {
				avatar = getString(sourceMap, "url")
				found = true
				break
			}
		}
		if !found && len(sources) > 0 {
			if firstSource, ok := sources[0].(map[string]interface{}); ok {
				avatar = getString(firstSource, "url")
			}
		}
	}
	return name, avatar
}

// resolvePlaylistCover returns the playlist cover URL from images/imagesV2,
// trying the first item's first source, then the top-level sources.
func resolvePlaylistCover(playlistData map[string]interface{}) string {
	imagesData := getMap(playlistData, "images")
	if len(imagesData) == 0 {
		imagesData = getMap(playlistData, "imagesV2")
	}
	if len(imagesData) == 0 {
		return ""
	}
	cover := ""
	if imageItems := getSlice(imagesData, "items"); len(imageItems) > 0 {
		if firstImage, ok := imageItems[0].(map[string]interface{}); ok {
			if firstSources := getSlice(firstImage, "sources"); len(firstSources) > 0 {
				if firstSource, ok := firstSources[0].(map[string]interface{}); ok {
					cover = getString(firstSource, "url")
				}
			}
		}
	}
	if cover == "" {
		if imageSources := getSlice(imagesData, "sources"); len(imageSources) > 0 {
			if firstSource, ok := imageSources[0].(map[string]interface{}); ok {
				cover = getString(firstSource, "url")
			}
		}
	}
	return cover
}

// buildPlaylistTrack maps one raw playlist content item to apiPlaylistTrack.
// ok is false for a missing track node or an empty title (both skipped upstream).
func buildPlaylistTrack(itemMap map[string]interface{}) (apiPlaylistTrack, bool) {
	trackData := getMap(getMap(itemMap, "itemV2"), "data")
	if len(trackData) == 0 {
		return apiPlaylistTrack{}, false
	}
	trackName := getString(trackData, "name")
	if trackName == "" {
		return apiPlaylistTrack{}, false
	}

	rank := ""
	status := ""
	for _, attr := range getSlice(itemMap, "attributes") {
		attrMap, ok := attr.(map[string]interface{})
		if !ok {
			continue
		}
		switch getString(attrMap, "key") {
		case "rank":
			rank = getString(attrMap, "value")
		case "status":
			status = getString(attrMap, "value")
		}
	}

	artistsData := getMap(trackData, "artists")
	trackArtistNames := artistNames(extractArtists(artistsData))
	artistIDs := []string{}
	for _, artistItem := range getSlice(artistsData, "items") {
		artistItemMap, ok := artistItem.(map[string]interface{})
		if !ok {
			continue
		}
		artistURI := getString(artistItemMap, "uri")
		if artistURI != "" && strings.Contains(artistURI, ":") {
			parts := strings.Split(artistURI, ":")
			if artistID := parts[len(parts)-1]; artistID != "" {
				artistIDs = append(artistIDs, artistID)
			}
		}
	}

	trackURI := getString(trackData, "uri")
	trackID := getString(trackData, "id")
	if trackID == "" && strings.Contains(trackURI, ":") {
		parts := strings.Split(trackURI, ":")
		trackID = parts[len(parts)-1]
	}

	albumData := getMap(trackData, "albumOfTrack")
	albumName := ""
	albumID := ""
	albumArtistsString := ""
	cover := ""
	if len(albumData) > 0 {
		albumName = getString(albumData, "name")
		if albumURI := getString(albumData, "uri"); strings.Contains(albumURI, ":") {
			parts := strings.Split(albumURI, ":")
			albumID = parts[len(parts)-1]
		}
		if coverObj := extractCoverImage(getMap(albumData, "coverArt")); coverObj != nil {
			cover = getString(coverObj, "small")
			if cover == "" {
				cover = getString(coverObj, "medium")
			}
			if cover == "" {
				cover = getString(coverObj, "large")
			}
		}
		if names := artistNames(extractArtists(getMap(albumData, "artists"))); len(names) > 0 {
			albumArtistsString = strings.Join(names, ", ")
		}
	}

	return apiPlaylistTrack{
		ID:          trackID,
		Cover:       cover,
		Title:       trackName,
		Artist:      strings.Join(trackArtistNames, ", "),
		ArtistIds:   artistIDs,
		Plays:       rank,
		Status:      status,
		Album:       albumName,
		AlbumArtist: albumArtistsString,
		AlbumID:     albumID,
		Duration:    getString(extractDuration(getFloat64(getMap(trackData, "trackDuration"), "totalMilliseconds")), "formatted"),
		IsExplicit:  getString(getMap(trackData, "contentRating"), "label") == "EXPLICIT",
		DiscNumber:  int(getFloat64(trackData, "discNumber")),
	}, true
}

// playlistFollowers reads the follower count, tolerating both the {totalCount}
// object form and a bare number.
func playlistFollowers(playlistData map[string]interface{}) int {
	followersData, exists := playlistData["followers"]
	if !exists {
		return 0
	}
	switch v := followersData.(type) {
	case map[string]interface{}:
		return int(getFloat64(v, "totalCount"))
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func extractRelease(release map[string]interface{}) (apiDiscographyItem, bool) {
	if len(release) == 0 {
		return apiDiscographyItem{}, false
	}

	dateInfo := getMap(release, "date")
	releaseDate := getString(dateInfo, "isoString")
	if releaseDate == "" && len(dateInfo) > 0 {
		yearStr := getString(dateInfo, "year")
		monthStr := getString(dateInfo, "month")
		dayStr := getString(dateInfo, "day")
		if yearStr != "" {
			if monthStr != "" && dayStr != "" {
				month, _ := strconv.Atoi(monthStr)
				day, _ := strconv.Atoi(dayStr)
				releaseDate = fmt.Sprintf("%s-%02d-%02d", yearStr, month, day)
			} else {
				releaseDate = yearStr
			}
		}
	} else if releaseDate != "" && strings.Contains(releaseDate, "T") {
		releaseDate = strings.Split(releaseDate, "T")[0]
	}

	cover := ""
	if coverObj := extractCoverImage(getMap(release, "coverArt")); coverObj != nil {
		cover = getString(coverObj, "medium")
	}

	releaseID := getString(release, "id")
	if releaseID == "" {
		if releaseURI := getString(release, "uri"); strings.Contains(releaseURI, ":") {
			parts := strings.Split(releaseURI, ":")
			releaseID = parts[len(parts)-1]
		}
	}

	return apiDiscographyItem{
		ID:          releaseID,
		Name:        getString(release, "name"),
		Cover:       cover,
		Date:        releaseDate,
		Year:        int(getFloat64(dateInfo, "year")),
		TotalTracks: int(getFloat64(getMap(release, "tracks"), "totalCount")),
		Type:        getString(release, "type"),
	}, true
}

func extractDiscographyItems(itemsData map[string]interface{}) []apiDiscographyItem {
	items := []apiDiscographyItem{}
	for _, item := range getSlice(itemsData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		releases := getMap(itemMap, "releases")
		var release map[string]interface{}
		if len(releases) > 0 {
			if releaseItems := getSlice(releases, "items"); len(releaseItems) > 0 {
				if releaseMap, ok := releaseItems[0].(map[string]interface{}); ok {
					release = releaseMap
				}
			}
		} else {
			release = getMap(itemMap, "album")
		}

		if len(release) > 0 {
			if extracted, ok := extractRelease(release); ok {
				items = append(items, extracted)
			}
		}
	}
	return items
}

func stripHTMLTags(s string) string {
	re := regexp.MustCompile(`<[^>]*>`)
	return re.ReplaceAllString(s, "")
}

// FilterArtist builds the typed artist contract directly from the raw
// artistUnion response (R2). The discography.all node is populated upstream by
// fetchArtistDiscography from a separate query before this runs.
func FilterArtist(data map[string]interface{}) apiArtistResponse {
	artistData := getMap(getMap(data, "data"), "artistUnion")
	if len(artistData) == 0 {
		return apiArtistResponse{}
	}

	var result apiArtistResponse

	profileRaw := getMap(artistData, "profile")
	if biographyMap := getMap(profileRaw, "biography"); len(biographyMap) > 0 {
		if biographyText := getString(biographyMap, "text"); biographyText != "" {
			result.Profile.Biography = html.UnescapeString(stripHTMLTags(biographyText))
		}
	}
	result.Profile.Name = getString(profileRaw, "name")
	result.Profile.Verified = getBool(profileRaw, "verified")
	result.Name = result.Profile.Name

	if headerData := getMap(getMap(artistData, "headerImage"), "data"); len(headerData) > 0 {
		if sources := getSlice(headerData, "sources"); len(sources) > 0 {
			if firstSource, ok := sources[0].(map[string]interface{}); ok {
				result.Header = getString(firstSource, "url")
			}
		}
	}

	statsRaw := getMap(artistData, "stats")
	result.Stats.Followers = int(getFloat64(statsRaw, "followers"))
	result.Stats.Listeners = int(getFloat64(statsRaw, "monthlyListeners"))
	result.Stats.Rank = int(getFloat64(statsRaw, "worldRank"))

	if allData := getMap(getMap(artistData, "discography"), "all"); len(allData) > 0 {
		result.Discography.All = extractDiscographyItems(allData)
		result.Discography.Total = int(getFloat64(allData, "totalCount"))
	}

	visualsData := getMap(artistData, "visuals")
	gallery := []string{}
	for _, item := range getSlice(getMap(visualsData, "gallery"), "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if sources := getSlice(itemMap, "sources"); len(sources) > 0 {
			if firstSource, ok := sources[0].(map[string]interface{}); ok {
				if galleryURL := getString(firstSource, "url"); galleryURL != "" {
					gallery = append(gallery, galleryURL)
				}
			}
		}
	}
	result.Gallery = gallery

	if avatarObj := extractCoverImage(getMap(visualsData, "avatarImage")); avatarObj != nil {
		if mediumURL := getString(avatarObj, "medium"); mediumURL != "" {
			result.Avatar = mediumURL
		} else if smallURL := getString(avatarObj, "small"); smallURL != "" {
			result.Avatar = smallURL
		}
	}

	if artistURI := getString(artistData, "uri"); strings.Contains(artistURI, ":") {
		parts := strings.Split(artistURI, ":")
		result.ID = parts[len(parts)-1]
	}

	return result
}

// FilterSearch builds the typed search contract directly from the raw searchV2
// response (R2), one section at a time. Input stays on the tolerant helpers;
// output is apiSearchResponse, so metadata.go no longer bridges through JSON.
func FilterSearch(data map[string]interface{}) apiSearchResponse {
	searchData := getMap(getMap(data, "data"), "searchV2")
	if len(searchData) == 0 {
		return apiSearchResponse{}
	}

	var result apiSearchResponse
	result.Results.Tracks = buildSearchTracks(searchData)
	result.Results.Albums = buildSearchAlbums(searchData)
	result.Results.Artists = buildSearchArtists(searchData)
	result.Results.Playlists = buildSearchPlaylists(searchData)
	result.TotalResults.Tracks = len(result.Results.Tracks)
	result.TotalResults.Albums = len(result.Results.Albums)
	result.TotalResults.Artists = len(result.Results.Artists)
	result.TotalResults.Playlists = len(result.Results.Playlists)
	return result
}

func buildSearchTracks(searchData map[string]interface{}) []apiSearchTrack {
	tracks := []apiSearchTrack{}
	tracksData := getMap(searchData, "tracksV2")
	if len(tracksData) == 0 {
		tracksData = getMap(searchData, "tracks")
	}
	for _, item := range getSlice(tracksData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var track map[string]interface{}
		if itemData, exists := itemMap["item"]; exists {
			if itemDataMap, ok := itemData.(map[string]interface{}); ok {
				track = getMap(itemDataMap, "data")
			}
		} else if trackData, exists := itemMap["track"]; exists {
			if trackMap, ok := trackData.(map[string]interface{}); ok {
				track = trackMap
			}
		}
		if len(track) == 0 {
			continue
		}
		trackName := getString(track, "name")
		if trackName == "" {
			continue
		}

		trackArtistNames := []string{}
		for _, artist := range extractArtists(getMap(track, "artists")) {
			trackArtistNames = append(trackArtistNames, getString(artist, "name"))
		}

		trackDurationMs := getFloat64(getMap(track, "duration"), "totalMilliseconds")
		if trackDurationMs == 0 {
			trackDurationMs = getFloat64(getMap(track, "trackDuration"), "totalMilliseconds")
		}

		albumData := getMap(track, "albumOfTrack")

		trackURI := getString(track, "uri")
		trackID := getString(track, "id")
		if trackID == "" && strings.Contains(trackURI, ":") {
			parts := strings.Split(trackURI, ":")
			trackID = parts[len(parts)-1]
		}

		cover := ""
		if coverObj := extractCoverImage(getMap(albumData, "coverArt")); coverObj != nil {
			cover = getString(coverObj, "medium")
		}

		tracks = append(tracks, apiSearchTrack{
			ID:         trackID,
			Name:       trackName,
			Artists:    strings.Join(trackArtistNames, ", "),
			Album:      getString(albumData, "name"),
			Duration:   getString(extractDuration(trackDurationMs), "formatted"),
			Cover:      cover,
			IsExplicit: getString(getMap(track, "contentRating"), "label") == "EXPLICIT",
		})
	}
	return tracks
}

func buildSearchAlbums(searchData map[string]interface{}) []apiSearchAlbum {
	albums := []apiSearchAlbum{}
	albumsData := getMap(searchData, "albumsV2")
	if len(albumsData) == 0 {
		albumsData = getMap(searchData, "albums")
	}
	for _, item := range getSlice(albumsData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var album map[string]interface{}
		if itemData, exists := itemMap["data"]; exists {
			if albumMap, ok := itemData.(map[string]interface{}); ok {
				album = albumMap
			}
		} else if albumData, exists := itemMap["album"]; exists {
			if albumMap, ok := albumData.(map[string]interface{}); ok {
				album = albumMap
			}
		}
		if len(album) == 0 {
			continue
		}

		albumArtistNames := []string{}
		for _, artist := range extractArtists(getMap(album, "artists")) {
			albumArtistNames = append(albumArtistNames, getString(artist, "name"))
		}
		albumArtistsString := strings.Join(albumArtistNames, ", ")

		albumName := getString(album, "name")
		if albumName == "" || albumArtistsString == "" {
			continue
		}

		albumURI := getString(album, "uri")
		albumID := getString(album, "id")
		if albumID == "" && strings.Contains(albumURI, ":") {
			parts := strings.Split(albumURI, ":")
			albumID = parts[len(parts)-1]
		}

		cover := ""
		if coverObj := extractCoverImage(getMap(album, "coverArt")); coverObj != nil {
			cover = getString(coverObj, "medium")
		}

		albums = append(albums, apiSearchAlbum{
			ID:      albumID,
			Name:    albumName,
			Artists: albumArtistsString,
			Cover:   cover,
			Year:    int(getFloat64(getMap(album, "date"), "year")),
		})
	}
	return albums
}

func buildSearchArtists(searchData map[string]interface{}) []apiSearchArtist {
	artists := []apiSearchArtist{}
	artistsData := getMap(searchData, "artistsV2")
	if len(artistsData) == 0 {
		artistsData = getMap(searchData, "artists")
	}
	for _, item := range getSlice(artistsData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var artist map[string]interface{}
		if itemData, exists := itemMap["data"]; exists {
			if artistMap, ok := itemData.(map[string]interface{}); ok {
				artist = artistMap
			}
		} else if artistData, exists := itemMap["artist"]; exists {
			if artistMap, ok := artistData.(map[string]interface{}); ok {
				artist = artistMap
			}
		}
		if len(artist) == 0 {
			continue
		}

		artistName := getString(getMap(artist, "profile"), "name")
		if artistName == "" {
			artistName = getString(artist, "name")
		}
		if artistName == "" {
			continue
		}

		artistURI := getString(artist, "uri")
		artistID := ""
		if strings.Contains(artistURI, ":") {
			parts := strings.Split(artistURI, ":")
			artistID = parts[len(parts)-1]
		}

		coverObj := extractCoverImage(getMap(artist, "visualIdentity"))
		if coverObj == nil {
			if visuals := getMap(artist, "visuals"); len(visuals) > 0 {
				coverObj = extractCoverImage(getMap(visuals, "avatarImage"))
			}
		}
		cover := ""
		if coverObj != nil {
			cover = getString(coverObj, "medium")
		}

		artists = append(artists, apiSearchArtist{ID: artistID, Name: artistName, Cover: cover})
	}
	return artists
}

func buildSearchPlaylists(searchData map[string]interface{}) []apiSearchPlaylist {
	playlists := []apiSearchPlaylist{}
	playlistsData := getMap(searchData, "playlistsV2")
	if len(playlistsData) == 0 {
		playlistsData = getMap(searchData, "playlists")
	}
	for _, item := range getSlice(playlistsData, "items") {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		var playlist map[string]interface{}
		if itemData, exists := itemMap["data"]; exists {
			if playlistMap, ok := itemData.(map[string]interface{}); ok {
				playlist = playlistMap
			}
		} else if playlistData, exists := itemMap["playlist"]; exists {
			if playlistMap, ok := playlistData.(map[string]interface{}); ok {
				playlist = playlistMap
			}
		}
		if len(playlist) == 0 {
			continue
		}

		playlistName := getString(playlist, "name")
		if playlistName == "" {
			continue
		}

		playlistURI := getString(playlist, "uri")
		playlistID := ""
		if strings.Contains(playlistURI, ":") {
			parts := strings.Split(playlistURI, ":")
			playlistID = parts[len(parts)-1]
		}

		playlistImages := getMap(playlist, "images")
		if len(playlistImages) == 0 {
			playlistImages = getMap(playlist, "imagesV2")
		}
		var playlistCoverObj map[string]interface{}
		if len(playlistImages) > 0 {
			imageItems := getSlice(playlistImages, "items")
			if len(imageItems) > 0 {
				if firstImage, ok := imageItems[0].(map[string]interface{}); ok {
					if firstSources := getSlice(firstImage, "sources"); firstSources != nil {
						playlistCoverObj = extractCoverImage(map[string]interface{}{"sources": firstSources})
					}
				}
			}
			if playlistCoverObj == nil {
				playlistCoverObj = extractCoverImage(playlistImages)
			}
		}
		cover := ""
		if playlistCoverObj != nil {
			cover = getString(playlistCoverObj, "medium")
		}

		playlists = append(playlists, apiSearchPlaylist{
			ID:    playlistID,
			Name:  playlistName,
			Cover: cover,
			Owner: getString(getMap(getMap(playlist, "ownerV2"), "data"), "name"),
		})
	}
	return playlists
}
