// Package isrclookup resolves a track's ISRC — the one identifier that is stable
// across Spotify, Tidal, Qobuz, Deezer and Amazon, and therefore the only thing
// the download path needs in order to find the same recording somewhere else.
//
// Two ways in, in order of authority:
//
//	Resolve(spotifyTrackID)        Spotify's own catalog record. Exact, cached.
//	ResolveByName(track, artist)   Deezer's public search. A name match, so it
//	                               can land on the wrong edition or remaster.
//
// It was called `songlink` until 2026-07-28, after the Song.link/Odesli client it
// was built around. Every call to that aggregator is gone (see item 7 of
// docs/dead-code-removal-plan.md) and the name outlived it by long enough to
// make the package look undeletable in an earlier audit — the import graph said
// five files depended on "Song.link" when none of them did.
//
// The package name deliberately avoids the bare word `isrc`: that is the right
// name for the *value*, used as a variable and a parameter throughout the
// download path, and a package by that name would shadow it at every call site.
package isrclookup

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// Client holds the HTTP client and the lazily-built Spotify client behind
// Resolve. There is normally one, from Shared().
//
// It carried a nine-calls-per-minute / seven-seconds-apart throttle until item 7:
// that existed for the Song.link API, which is no longer called. Spotify's client
// handles its own tokens, and the Deezer fallback is a single unmetered request,
// so neither needs pacing.
type Client struct {
	client *http.Client
	mu     sync.Mutex // guards spotifyClient
	// spotifyClient is lazily created on first ISRC-direct lookup and reused
	// across all subsequent ones (see getSpotifyClient) so we pay the TOTP
	// token handshake once per Client instead of once per track. The
	// client caches and auto-refreshes its own token internally.
	spotifyClient *spotify.SpotifyClient
}

var sharedClient *Client

// Shared retourne le singleton global (thread-safe via init)
func Shared() *Client {
	if sharedClient == nil {
		sharedClient = New()
	}
	return sharedClient
}

func New() *Client {
	return &Client{
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

	slog.Debug("[ISRC] Found ISRC from Deezer", "isrc", deezerTrack.ISRC, "track", deezerTrack.Title)
	return deezerTrack.ISRC, nil
}

// Resolve resolves a Spotify track's ISRC straight from Spotify's own
// metadata, bypassing the cross-provider name matching ResolveByName
// relies on. Cached in the shared ISRC cache (see isrc_cache.go) to avoid
// re-resolving the same track on every retag/redownload.
func (s *Client) Resolve(spotifyTrackID string) (string, error) {
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
		slog.Debug("[ISRC] failed to cache direct ISRC", "err", err)
	}

	return isrc, nil
}

// getSpotifyClient returns the shared, lazily-initialized Spotify client used
// for ISRC-direct lookups. Guarded by s.mu (a quick nil-check + assignment);
// the returned client does its own token locking internally, so callers must
// invoke it outside any network round-trip.
func (s *Client) getSpotifyClient() *spotify.SpotifyClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spotifyClient == nil {
		s.spotifyClient = spotify.NewSpotifyClient()
	}
	return s.spotifyClient
}

// ResolveByName resolves an ISRC by name search against Deezer's
// public API (no key required). It used to return a *SongLinkURLs carrying
// TidalURL and AmazonURL fields it never actually filled — that struct is gone
// along with the rest of Song.link; the ISRC was always the whole answer.
func ResolveByName(trackName, artistName string) (string, error) {
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

	slog.Debug("[ISRC] Found ISRC", "isrc", isrc, "track", trackName, "artist", artistName)
	return isrc, nil
}
