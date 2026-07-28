package songlink

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// SongLinkClient no longer talks to Song.link — item 7 removed every call. What
// survives is the ISRC resolver it also happened to own: a cached, Spotify-direct
// lookup. The nine-calls-per-minute / seven-seconds-apart throttle went with the
// caller it existed for; Spotify's own client does its own token handling, and
// the Deezer fallback is a single unmetered request.
type SongLinkClient struct {
	client *http.Client
	mu     sync.Mutex // guards spotifyClient
	// spotifyClient is lazily created on first ISRC-direct lookup and reused
	// across all subsequent ones (see getSpotifyClient) so we pay the TOTP
	// token handshake once per SongLinkClient instead of once per track. The
	// client caches and auto-refreshes its own token internally.
	spotifyClient *spotify.SpotifyClient
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
		client: util.NewHTTPClient(30 * time.Second),
	}
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
// metadata, bypassing the cross-provider name matching GetDeezerSearchFallback
// relies on. Cached in the shared ISRC cache (see isrc_cache.go) to avoid
// re-resolving the same track on every retag/redownload.
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

// GetDeezerSearchFallback resolves an ISRC by name search against Deezer's
// public API (no key required). It used to return a *SongLinkURLs carrying
// TidalURL and AmazonURL fields it never actually filled — the struct is gone
// and so is the confusion; the ISRC was always the whole answer.
func GetDeezerSearchFallback(trackName, artistName string) (string, error) {
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
		return "", fmt.Errorf("deezer search failed: %w", err)
	}
	defer resp.Body.Close()

	var searchResp struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return "", fmt.Errorf("deezer search decode failed: %w", err)
	}
	if len(searchResp.Data) == 0 {
		return "", fmt.Errorf("deezer search: no results for %s - %s", trackName, artistName)
	}

	trackID := searchResp.Data[0].ID
	if trackID == 0 {
		return "", fmt.Errorf("deezer: no track found for %s - %s", trackName, artistName)
	}

	// Construire l'URL du track Deezer et réutiliser getDeezerISRC (déjà présente dans ce fichier)
	deezerTrackURL := fmt.Sprintf("https://www.deezer.com/track/%d", trackID)
	isrc, err := getDeezerISRC(deezerTrackURL)
	if err != nil || isrc == "" {
		return "", fmt.Errorf("deezer: failed to get ISRC for track %d (%s - %s): %v", trackID, trackName, artistName, err)
	}

	slog.Debug("[Deezer fallback] Found ISRC", "isrc", isrc, "track", trackName, "artist", artistName)
	return isrc, nil
}
