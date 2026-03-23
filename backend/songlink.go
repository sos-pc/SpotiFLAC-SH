package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type SongLinkClient struct {
	client            *http.Client
	lastAPICallTime   time.Time
	apiCallCount      int
	apiCallResetTime  time.Time
	rateLimitedUntil  time.Time // si non-zero, skip les appels jusqu'Ã  cette heure
}

// isRateLimited retourne true si on est en fenÃªtre de rate limit
func (s *SongLinkClient) IsRateLimited() bool {
	return s.isRateLimited()
}

func (s *SongLinkClient) RateLimitedUntil() time.Time {
	return s.rateLimitedUntil
}

func (s *SongLinkClient) isRateLimited() bool {
	return !s.rateLimitedUntil.IsZero() && time.Now().Before(s.rateLimitedUntil)
}

// markRateLimited enregistre un 429 et bloque les appels pendant 60s
func (s *SongLinkClient) markRateLimited() {
	s.rateLimitedUntil = time.Now().Add(5 * time.Minute)
	fmt.Printf("[Songlink] Rate limited â€” skipping calls for 60s\n")
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
		client: NewHTTPClient(30 * time.Second),
		apiCallResetTime: time.Now(),
	}
}

func (s *SongLinkClient) GetAllURLsFromSpotify(spotifyTrackID string, region string) (*SongLinkURLs, error) {
	if s.isRateLimited() {
		return nil, fmt.Errorf("songlink rate limited, skipping call")
	}
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}

	if s.apiCallCount >= 9 {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		if waitTime > 0 {
			fmt.Printf("Rate limit reached, waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
			s.apiCallCount = 0
			s.apiCallResetTime = time.Now()
		}
	}

	if !s.lastAPICallTime.IsZero() {
		timeSinceLastCall := now.Sub(s.lastAPICallTime)
		minDelay := 7 * time.Second
		if timeSinceLastCall < minDelay {
			waitTime := minDelay - timeSinceLastCall
			fmt.Printf("Rate limiting: waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
		}
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

	fmt.Println("Getting streaming URLs from song.link...")

	maxRetries := 3
	var resp *http.Response
	for i := 0; i < maxRetries; i++ {
		resp, err = s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to get URLs: %w", err)
		}

		s.lastAPICallTime = time.Now()
		s.apiCallCount++

		if resp.StatusCode == 429 {
			resp.Body.Close()
			s.markRateLimited()
			return nil, fmt.Errorf("API returned status 429")
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
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
		fmt.Printf("âœ“ Tidal URL found\n")
	}

	if amazonLink, ok := songLinkResp.LinksByPlatform["amazonMusic"]; ok && amazonLink.URL != "" {
		amazonURL := amazonLink.URL

		if len(amazonURL) > 0 {
			urls.AmazonURL = amazonURL
			fmt.Printf("âœ“ Amazon URL found\n")
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
	if s.isRateLimited() {
		return nil, fmt.Errorf("songlink rate limited, skipping call")
	}
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}

	if s.apiCallCount >= 9 {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		if waitTime > 0 {
			fmt.Printf("Rate limit reached, waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
			s.apiCallCount = 0
			s.apiCallResetTime = time.Now()
		}
	}

	if !s.lastAPICallTime.IsZero() {
		timeSinceLastCall := now.Sub(s.lastAPICallTime)
		minDelay := 7 * time.Second
		if timeSinceLastCall < minDelay {
			waitTime := minDelay - timeSinceLastCall
			fmt.Printf("Rate limiting: waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
		}
	}

	spotifyURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)

	apiURL := fmt.Sprintf("https://api.song.link/v1-alpha.1/links?url=%s", url.QueryEscape(spotifyURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	fmt.Printf("Checking availability for track: %s\n", spotifyTrackID)

	maxRetries := 3
	var resp *http.Response
	for i := 0; i < maxRetries; i++ {
		resp, err = s.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to check availability: %w", err)
		}

		s.lastAPICallTime = time.Now()
		s.apiCallCount++

		if resp.StatusCode == 429 {
			resp.Body.Close()
			s.markRateLimited()
			return nil, fmt.Errorf("API returned status 429")
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
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
	client := NewHTTPClient(10 * time.Second)
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

func (s *SongLinkClient) GetDeezerURLFromSpotify(spotifyTrackID string) (string, error) {
	if s.isRateLimited() {
		return "", fmt.Errorf("songlink rate limited, skipping call")
	}
	now := time.Now()
	if now.Sub(s.apiCallResetTime) >= time.Minute {
		s.apiCallCount = 0
		s.apiCallResetTime = now
	}

	if s.apiCallCount >= 9 {
		waitTime := time.Minute - now.Sub(s.apiCallResetTime)
		if waitTime > 0 {
			fmt.Printf("Rate limit reached, waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
			s.apiCallCount = 0
			s.apiCallResetTime = time.Now()
		}
	}

	if !s.lastAPICallTime.IsZero() {
		timeSinceLastCall := now.Sub(s.lastAPICallTime)
		minDelay := 7 * time.Second
		if timeSinceLastCall < minDelay {
			waitTime := minDelay - timeSinceLastCall
			fmt.Printf("Rate limiting: waiting %v...\n", waitTime.Round(time.Second))
			time.Sleep(waitTime)
		}
	}

	spotifyURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)

	apiURL := fmt.Sprintf("https://api.song.link/v1-alpha.1/links?url=%s", url.QueryEscape(spotifyURL))

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	fmt.Println("Getting Deezer URL from song.link...")

	maxRetries := 3
	var resp *http.Response
	for i := 0; i < maxRetries; i++ {
		resp, err = s.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to get Deezer URL: %w", err)
		}

		s.lastAPICallTime = time.Now()
		s.apiCallCount++

		if resp.StatusCode == 429 {
			resp.Body.Close()
			s.markRateLimited()
			return "", fmt.Errorf("API returned status 429")
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return "", fmt.Errorf("API returned status %d", resp.StatusCode)
		}

		break
	}
	defer resp.Body.Close()

	var songLinkResp struct {
		LinksByPlatform map[string]struct {
			URL string `json:"url"`
		} `json:"linksByPlatform"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&songLinkResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	deezerLink, ok := songLinkResp.LinksByPlatform["deezer"]
	if !ok || deezerLink.URL == "" {
		return "", fmt.Errorf("deezer link not found")
	}

	deezerURL := deezerLink.URL
	fmt.Printf("Found Deezer URL: %s\n", deezerURL)
	return deezerURL, nil
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

	client := NewHTTPClient(10 * time.Second)
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

	fmt.Printf("Found ISRC from Deezer: %s (track: %s)\n", deezerTrack.ISRC, deezerTrack.Title)
	return deezerTrack.ISRC, nil
}

func (s *SongLinkClient) GetISRC(spotifyID string) (string, error) {
	deezerURL, err := s.GetDeezerURLFromSpotify(spotifyID)
	if err != nil {
		return "", err
	}
	return getDeezerISRC(deezerURL)
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

	client := NewHTTPClient(10 * time.Second)
	resp, err := client.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("deezer search failed: %w", err)
	}
	defer resp.Body.Close()

	var searchResp struct {
		Data []struct {
			ID   int64  `json:"id"`
			ISRC string `json:"isrc"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("deezer search decode failed: %w", err)
	}
	if len(searchResp.Data) == 0 {
		return nil, fmt.Errorf("deezer search: no results for %s - %s", trackName, artistName)
	}

	isrc := searchResp.Data[0].ISRC
	if isrc == "" {
		return nil, fmt.Errorf("deezer: no ISRC in search result for %s - %s", trackName, artistName)
	}

	fmt.Printf("[Deezer fallback] Found ISRC %s for %s - %s\n", isrc, trackName, artistName)
	return &SongLinkURLs{ISRC: isrc}, nil
}

// ScrapeSongLinkHTML — bypass rate-limit via scraping de la page song.link
// Utilise https://song.link/s/{spotifyID} et parse le blob __NEXT_DATA__ Next.js
// qui contient linksByPlatform avec la même structure que l'API JSON — endpoint
// distinct donc soumis à un quota différent.
func (s *SongLinkClient) ScrapeSongLinkHTML(spotifyTrackID string) (*SongLinkURLs, error) {
	pageURL := fmt.Sprintf("https://song.link/s/%s", spotifyTrackID)

	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("scrape: failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		s.markRateLimited()
		return nil, fmt.Errorf("scrape: song.link returned 429 (rate limited)")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("scrape: song.link returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("scrape: failed to read body: %w", err)
	}

	html := string(body)

	// Extraire le blob JSON __NEXT_DATA__ embarqué dans la page Next.js
	const marker = `__NEXT_DATA__" type="application/json">`
	start := strings.Index(html, marker)
	if start == -1 {
		return nil, fmt.Errorf("scrape: __NEXT_DATA__ not found in song.link page")
	}
	start += len(marker)
	end := strings.Index(html[start:], "</script>")
	if end == -1 {
		return nil, fmt.Errorf("scrape: __NEXT_DATA__ closing tag not found")
	}
	jsonRaw := html[start : start+end]

	// Structure du blob Next.js de song.link
	var nextData struct {
		Props struct {
			PageProps struct {
				Error *struct {
					StatusCode int `json:"statusCode"`
				} `json:"error"`
				PageData *struct {
					LinksByPlatform map[string]struct {
						URL string `json:"url"`
					} `json:"linksByPlatform"`
				} `json:"pageData"`
			} `json:"pageProps"`
		} `json:"props"`
	}

	if err := json.Unmarshal([]byte(jsonRaw), &nextData); err != nil {
		return nil, fmt.Errorf("scrape: failed to parse __NEXT_DATA__: %w", err)
	}

	// Vérifier si song.link a retourné une erreur dans le JSON (ex: 429 interne)
	if nextData.Props.PageProps.Error != nil {
		code := nextData.Props.PageProps.Error.StatusCode
		if code == 429 {
			s.markRateLimited()
			return nil, fmt.Errorf("scrape: song.link NEXT_DATA error 429 (rate limited)")
		}
		return nil, fmt.Errorf("scrape: song.link NEXT_DATA error %d", code)
	}

	if nextData.Props.PageProps.PageData == nil {
		return nil, fmt.Errorf("scrape: no pageData in song.link NEXT_DATA")
	}

	links := nextData.Props.PageProps.PageData.LinksByPlatform
	if len(links) == 0 {
		return nil, fmt.Errorf("scrape: no linksByPlatform in song.link NEXT_DATA for %s", spotifyTrackID)
	}

	urls := &SongLinkURLs{}
	found := false

	if tidalLink, ok := links["tidal"]; ok && tidalLink.URL != "" {
		urls.TidalURL = tidalLink.URL
		found = true
		fmt.Printf("[Songlink HTML] ✓ Tidal: %s\n", tidalLink.URL)
	}

	if amazonLink, ok := links["amazonMusic"]; ok && amazonLink.URL != "" {
		urls.AmazonURL = amazonLink.URL
		found = true
		fmt.Printf("[Songlink HTML] ✓ Amazon: %s\n", amazonLink.URL)
	}

	if deezerLink, ok := links["deezer"]; ok && deezerLink.URL != "" {
		if isrc, iErr := getDeezerISRC(deezerLink.URL); iErr == nil && isrc != "" {
			urls.ISRC = isrc
			found = true
			fmt.Printf("[Songlink HTML] ✓ ISRC via Deezer: %s\n", isrc)
		}
	}

	if !found {
		return nil, fmt.Errorf("scrape: no usable URLs in song.link NEXT_DATA for %s", spotifyTrackID)
	}

	return urls, nil
}
