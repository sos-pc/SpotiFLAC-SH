package songlink

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

type SongLinkClient struct {
	client           *http.Client
	mu               sync.Mutex // protects all fields below
	lastAPICallTime  time.Time
	apiCallCount     int
	apiCallResetTime time.Time
	rateLimitedUntil time.Time
	// spotifyClient is lazily created on first ISRC-direct lookup and reused
	// across all subsequent ones (see getSpotifyClient) so we pay the TOTP
	// token handshake once per SongLinkClient instead of once per track. The
	// client caches and auto-refreshes its own token internally.
	spotifyClient *spotify.SpotifyClient
}

// isRateLimited must be called with s.mu held.
func (s *SongLinkClient) isRateLimited() bool {
	return !s.rateLimitedUntil.IsZero() && time.Now().Before(s.rateLimitedUntil)
}

func (s *SongLinkClient) IsRateLimited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isRateLimited()
}

func (s *SongLinkClient) RateLimitedUntil() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rateLimitedUntil
}

// markRateLimited must be called with s.mu held.
func (s *SongLinkClient) markRateLimited() {
	s.rateLimitedUntil = time.Now().Add(5 * time.Minute)
	slog.Warn("[Songlink] Rate limited, skipping calls for 5 minutes")
}

type SongLinkURLs struct {
	TidalURL  string `json:"tidal_url"`
	AmazonURL string `json:"amazon_url"`
	ISRC      string `json:"isrc"`
}

type TrackAvailability struct {
	SpotifyID string `json:"spotify_id"`
	Tidal     bool   `json:"tidal"`
	Amazon    bool   `json:"amazon"`
	Qobuz     bool   `json:"qobuz"`
	Deezer    bool   `json:"deezer"`
	TidalURL  string `json:"tidal_url,omitempty"`
	AmazonURL string `json:"amazon_url,omitempty"`
	QobuzURL  string `json:"qobuz_url,omitempty"`
	DeezerURL string `json:"deezer_url,omitempty"`
}

var globalSongLinkClient *SongLinkClient

// GetSongLinkClient retourne le singleton global (thread-safe via init)
func GetSongLinkClient() *SongLinkClient {
	if globalSongLinkClient == nil {
		globalSongLinkClient = NewSongLinkClient()
	}
	return globalSongLinkClient
}

func NewSongLinkClient() *SongLinkClient {
	return &SongLinkClient{
		client:           util.NewHTTPClient(30 * time.Second),
		apiCallResetTime: time.Now(),
	}
}

// acquireSlot enforces rate-limiting before an API call:
// - Returns an error immediately if hard-rate-limited (429 cooldown).
// - Resets the per-minute counter after each minute window.
// - Blocks until the window resets if 9 calls have been made this minute.
// - Enforces a minimum 7-second delay between consecutive calls.
func (s *SongLinkClient) acquireSlot() error {
	s.mu.Lock()
	if s.isRateLimited() {
		s.mu.Unlock()
		return fmt.Errorf("songlink rate limited, skipping call")
	}
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}
	if s.apiCallCount >= 9 {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		s.mu.Unlock()
		if waitTime > 0 {
			slog.Debug("[Songlink] Rate limit reached, waiting", "wait", waitTime.Round(time.Second))
			time.Sleep(waitTime)
		}
		s.mu.Lock()
		s.apiCallCount = 0
		s.apiCallResetTime = time.Now()
	}
	lastCall := s.lastAPICallTime
	s.mu.Unlock()
	if !lastCall.IsZero() {
		const minDelay = 7 * time.Second
		if elapsed := time.Since(lastCall); elapsed < minDelay {
			wait := minDelay - elapsed
			slog.Debug("[Songlink] Rate limiting, waiting", "wait", wait.Round(time.Second))
			time.Sleep(wait)
		}
	}
	return nil
}

func (s *SongLinkClient) GetAllURLsFromSpotify(spotifyTrackID string, region string) (*SongLinkURLs, error) {
	if err := s.acquireSlot(); err != nil {
		return nil, err
	}

	spotifyURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)

	apiURL := fmt.Sprintf("https://api.song.link/v1-alpha.1/links?url=%s", url.QueryEscape(spotifyURL))

	if region != "" {
		apiURL += fmt.Sprintf("&userCountry=%s", region)
	}

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	slog.Debug("[Songlink] Getting streaming URLs from song.link")

	maxRetries := 3
	var resp *http.Response
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = s.client.Do(req)
		if err != nil {
			// Network error — worth a retry (transient DNS/connection
			// hiccup), unlike a 429 or a non-5xx status below.
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, fmt.Errorf("failed to get URLs: %w", err)
		}

		s.mu.Lock()
		s.lastAPICallTime = time.Now()
		s.apiCallCount++
		s.mu.Unlock()

		if resp.StatusCode == 429 {
			resp.Body.Close()
			s.mu.Lock()
			s.markRateLimited()
			s.mu.Unlock()
			return nil, fmt.Errorf("songlink: API returned status 429")
		}

		if resp.StatusCode >= 500 && attempt < maxRetries-1 {
			// Transient server error — retry; a permanent 4xx below isn't
			// worth retrying.
			resp.Body.Close()
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("songlink: API returned status %d", resp.StatusCode)
		}

		break
	}
	defer resp.Body.Close()

	var songLinkResp struct {
		LinksByPlatform map[string]struct {
			URL string `json:"url"`
		} `json:"linksByPlatform"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	if err := json.Unmarshal(body, &songLinkResp); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	urls := &SongLinkURLs{}

	if tidalLink, ok := songLinkResp.LinksByPlatform["tidal"]; ok && tidalLink.URL != "" {
		urls.TidalURL = tidalLink.URL
		slog.Debug("[Songlink] Tidal URL found")
	}

	if amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]; ok && amazonLink.URL != "" {
		amazonURL := amazonLink.URL

		if len(amazonURL) > 0 {
			urls.AmazonURL = amazonURL
			slog.Debug("[Songlink] Amazon URL found")
		}
	}

	if deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]; ok && deezerLink.URL != "" {
		if isrc, err := getDeezerISRC(deezerLink.URL); err == nil && isrc != "" {
			urls.ISRC = isrc
		}
	}

	if urls.TidalURL == "" && urls.AmazonURL == "" {
		return nil, fmt.Errorf("no streaming URLs found")
	}

	return urls, nil
}

func (s *SongLinkClient) CheckTrackAvailability(spotifyTrackID string) (*TrackAvailability, error) {
	if err := s.acquireSlot(); err != nil {
		return nil, err
	}

	spotifyURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)

	apiURL := fmt.Sprintf("https://api.song.link/v1-alpha.1/links?url=%s", url.QueryEscape(spotifyURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	slog.Debug("[Songlink] Checking availability for track", "spotify_id", spotifyTrackID)

	maxRetries := 3
	var resp *http.Response
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err = s.client.Do(req)
		if err != nil {
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, fmt.Errorf("failed to check availability: %w", err)
		}

		s.mu.Lock()
		s.lastAPICallTime = time.Now()
		s.apiCallCount++
		s.mu.Unlock()

		if resp.StatusCode == 429 {
			resp.Body.Close()
			s.mu.Lock()
			s.markRateLimited()
			s.mu.Unlock()
			return nil, fmt.Errorf("songlink: API returned status 429")
		}

		if resp.StatusCode >= 500 && attempt < maxRetries-1 {
			resp.Body.Close()
			time.Sleep(time.Duration(attempt+1) * time.Second)
			continue
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("songlink: API returned status %d", resp.StatusCode)
		}

		break
	}
	defer resp.Body.Close()

	var songLinkResp struct {
		LinksByPlatform map[string]struct {
			URL string `json:"url"`
		} `json:"linksByPlatform"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("API returned empty response")
	}

	if err := json.Unmarshal(body, &songLinkResp); err != nil {

		bodyStr := string(body)
		if len(bodyStr) > 200 {
			bodyStr = bodyStr[:200] + "..."
		}
		return nil, fmt.Errorf("failed to decode response: %w (response: %s)", err, bodyStr)
	}

	availability := &TrackAvailability{
		SpotifyID: spotifyTrackID,
	}

	if tidalLink, ok := songLinkResp.LinksByPlatform["tidal"]; ok && tidalLink.URL != "" {
		availability.Tidal = true
		availability.TidalURL = tidalLink.URL
	}

	if amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]; ok && amazonLink.URL != "" {
		availability.Amazon = true
		availability.AmazonURL = amazonLink.URL
	}

	if deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]; ok && deezerLink.URL != "" {
		deezerURL := deezerLink.URL
		availability.Deezer = true
		availability.DeezerURL = deezerURL

		deezerISRC, err := getDeezerISRC(deezerURL)
		if err == nil && deezerISRC != "" {
			qobuzAvailable := checkQobuzAvailability(deezerISRC)
			availability.Qobuz = qobuzAvailable
		}
	}

	return availability, nil
}

func checkQobuzAvailability(isrc string) bool {
	client := util.NewHTTPClient(10 * time.Second)
	appID := "798273057"

	searchURL := fmt.Sprintf("https://www.qobuz.com/api.json/0.2/track/search?query=%s&limit=1&app_id=%s", isrc, appID)

	resp, err := client.Get(searchURL)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return false
	}

	var searchResp struct {
		Tracks struct {
			Total int `json:"total"`
		} `json:"tracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return false
	}

	return searchResp.Tracks.Total > 0
}

func getDeezerISRC(deezerURL string) (string, error) {

	var trackID string
	if strings.Contains(deezerURL, "/track/") {
		parts := strings.Split(deezerURL, "/track/")
		if len(parts) > 1 {
			trackID = strings.Split(parts[1], "?")[0]
			trackID = strings.TrimSpace(trackID)
		}
	}

	if trackID == "" {
		return "", fmt.Errorf("could not extract track ID from Deezer URL: %s", deezerURL)
	}

	apiURL := fmt.Sprintf("https://api.deezer.com/track/%s", trackID)

	client := util.NewHTTPClient(10 * time.Second)
	resp, err := client.Get(apiURL)
	if err != nil {
		return "", fmt.Errorf("failed to call Deezer API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("Deezer API returned status %d", resp.StatusCode)
	}

	var deezerTrack struct {
		ID    int64  `json:"id"`
		ISRC  string `json:"isrc"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deezerTrack); err != nil {
		return "", fmt.Errorf("failed to decode Deezer API response: %w", err)
	}

	if deezerTrack.ISRC == "" {
		return "", fmt.Errorf("ISRC not found in Deezer API response for track %s", trackID)
	}

	slog.Debug("[Songlink] Found ISRC from Deezer", "isrc", deezerTrack.ISRC, "track", deezerTrack.Title)
	return deezerTrack.ISRC, nil
}

// GetISRCDirect resolves a Spotify track's ISRC straight from Spotify's own
// metadata (via the anonymous session token already used for Song.link
// lookups elsewhere in this package), bypassing the cross-provider name
// matching that GetDeezerURLFromSpotify/getDeezerISRC rely on. Cached in the
// shared ISRC cache (see isrc_cache.go) to avoid re-resolving the same track
// on every retag/redownload.
func (s *SongLinkClient) GetISRCDirect(spotifyTrackID string) (string, error) {
	spotifyTrackID = strings.TrimSpace(spotifyTrackID)
	if spotifyTrackID == "" {
		return "", fmt.Errorf("spotify track ID is required")
	}

	if cached, err := GetCachedISRC(spotifyTrackID); err == nil && cached != "" {
		return cached, nil
	}

	isrc, err := s.getSpotifyClient().GetTrackISRC(spotifyTrackID)
	if err != nil {
		return "", err
	}

	if err := PutCachedISRC(spotifyTrackID, isrc); err != nil {
		slog.Debug("[Songlink] failed to cache direct ISRC", "err", err)
	}

	return isrc, nil
}

// getSpotifyClient returns the shared, lazily-initialized Spotify client used
// for ISRC-direct lookups. Guarded by s.mu (a quick nil-check + assignment);
// the returned client does its own token locking internally, so callers must
// invoke it outside any network round-trip.
func (s *SongLinkClient) getSpotifyClient() *spotify.SpotifyClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spotifyClient == nil {
		s.spotifyClient = spotify.NewSpotifyClient()
	}
	return s.spotifyClient
}

// GetDeezerSearchFallback â€” fallback quand Songlink est rate-limited
// Cherche la track via l'API Deezer publique (pas de clÃ© requise)
// et retourne l'ISRC pour que qobuz.go puisse tÃ©lÃ©charger
func GetDeezerSearchFallback(trackName, artistName string) (*SongLinkURLs, error) {
	// Premier artiste seulement pour Ã©viter les Ã©checs sur les collaborations
	cleanArtist := artistName
	for _, sep := range []string{", ", " & ", " feat.", " ft.", " featuring "} {
		if idx := strings.Index(cleanArtist, sep); idx > 0 {
			cleanArtist = strings.TrimSpace(cleanArtist[:idx])
			break
		}
	}
	// Nettoyer le nom de track : supprimer suffixes type "- 2003 Remaster", "- Written By X"
	cleanTrack := trackName
	for _, sep := range []string{" - ", " (", " ["} {
		if idx := strings.Index(cleanTrack, sep); idx > 0 {
			cleanTrack = strings.TrimSpace(cleanTrack[:idx])
			break
		}
	}
	query := url.QueryEscape(cleanTrack + " " + cleanArtist)
	searchURL := fmt.Sprintf("https://api.deezer.com/search?q=%s&limit=1", query)

	client := util.NewHTTPClient(10 * time.Second)
	resp, err := client.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("deezer search failed: %w", err)
	}
	defer resp.Body.Close()

	var searchResp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("deezer search decode failed: %w", err)
	}
	if len(searchResp.Data) == 0 {
		return nil, fmt.Errorf("deezer search: no results for %s - %s", trackName, artistName)
	}

	trackID := searchResp.Data[0].ID
	if trackID == 0 {
		return nil, fmt.Errorf("deezer: no track found for %s - %s", trackName, artistName)
	}

	// Construire l'URL du track Deezer et réutiliser getDeezerISRC (déjà présente dans ce fichier)
	deezerTrackURL := fmt.Sprintf("https://www.deezer.com/track/%d", trackID)
	isrc, err := getDeezerISRC(deezerTrackURL)
	if err != nil || isrc == "" {
		return nil, fmt.Errorf("deezer: failed to get ISRC for track %d (%s - %s): %v", trackID, trackName, artistName, err)
	}

	slog.Debug("[Deezer fallback] Found ISRC", "isrc", isrc, "track", trackName, "artist", artistName)
	return &SongLinkURLs{ISRC: isrc}, nil
}
