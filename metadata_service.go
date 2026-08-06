package main

// ─────────────────────────────────────────────────────────────────────────────
// MetadataService — Spotify metadata and search, carved out of the former App
// god-object (R3). Streaming-link resolution and track-availability lookups also
// lived here until item 7b removed them with the rest of Song.link.
//
// Holds only its real dependency: AuthManager, for EffectiveDownloadSettings'
// per-user lookup (see GetSpotifyMetadata / DownloadSettings, R8).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
)

// The JobManager dependency went with them: it was held only to borrow the
// JobManager's shared ISRC client.
type MetadataService struct {
	auth *auth.AuthManager
}

func NewMetadataService(auth *auth.AuthManager) *MetadataService {
	return &MetadataService{auth: auth}
}

type SpotifyMetadataRequest struct {
	URL     string  `json:"url"`
	Batch   bool    `json:"batch"`
	Delay   float64 `json:"delay"`
	Timeout float64 `json:"timeout"`
}

type SpotifySearchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type SpotifySearchByTypeRequest struct {
	Query      string `json:"query"`
	SearchType string `json:"search_type"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
}

// normalizeSpotifyURL supprime le préfixe intl-xx/ et le paramètre ?si=
// ex: https://open.spotify.com/intl-fr/album/ID?si=xxx → https://open.spotify.com/album/ID
func normalizeSpotifyURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	// Supprimer query params (si=, etc.)
	parsed.RawQuery = ""
	// Supprimer segment intl-xx
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if !strings.HasPrefix(p, "intl-") {
			filtered = append(filtered, p)
		}
	}
	parsed.Path = "/" + strings.Join(filtered, "/")
	return parsed.String()
}

// GetSpotifyMetadata fetches track/album/playlist/artist metadata natively,
// falling back to userID's effective SpotFetch API URL (their own saved
// settings if any, else the operator's global config.json — see
// EffectiveDownloadSettings) if the native client fails. userID == ""
// resolves to the global settings.
func (m *MetadataService) GetSpotifyMetadata(req SpotifyMetadataRequest, userID string) (string, error) {
	if req.URL == "" {
		return "", fmt.Errorf("URL parameter is required")
	}
	// Normaliser l'URL : supprimer intl-xx/ et ?si=... pour compatibilité API externe
	req.URL = normalizeSpotifyURL(req.URL)
	if req.Delay == 0 {
		req.Delay = 1.0
	}
	if req.Timeout == 0 {
		req.Timeout = 300.0
	}

	metaCtx, metaCancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout*float64(time.Second)))
	defer metaCancel()

	spotFetchAPIURL := settings.EffectiveDownloadSettings(m.auth, userID).SpotFetchAPIURL

	// Client natif Spotify (TOTP) — avec fallback automatique vers SpotFetch si échec
	data, nativeErr := spotify.GetFilteredSpotifyData(metaCtx, req.URL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
	if nativeErr == nil {
		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to encode response: %v", err)
		}
		return string(jsonData), nil
	}

	// Fallback automatique vers SpotFetch si disponible
	if spotFetchAPIURL != "" {
		data, err := spotify.GetSpotifyDataWithAPI(metaCtx, req.URL, true, spotFetchAPIURL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
		if err != nil {
			return "", fmt.Errorf("failed to fetch metadata (native: %v, spotfetch: %v)", nativeErr, err)
		}
		jsonData, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return "", fmt.Errorf("failed to encode response: %v", err)
		}
		return string(jsonData), nil
	}

	return "", fmt.Errorf("failed to fetch metadata: %v", nativeErr)
}

func (m *MetadataService) SearchSpotify(req SpotifySearchRequest) (*spotify.SearchResponse, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return spotify.SearchSpotify(ctx, req.Query, req.Limit)
}

func (m *MetadataService) SearchSpotifyByType(req SpotifySearchByTypeRequest) ([]spotify.SearchResult, error) {
	if req.Query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	if req.SearchType == "" {
		return nil, fmt.Errorf("search type is required")
	}
	if req.Limit <= 0 {
		req.Limit = 50
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return spotify.SearchSpotifyByType(ctx, req.Query, req.SearchType, req.Limit, req.Offset)
}

func (m *MetadataService) GetPreviewURL(trackID string) (string, error) {
	return spotify.GetPreviewURL(trackID)
}
