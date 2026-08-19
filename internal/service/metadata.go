package service

// ─────────────────────────────────────────────────────────────────────────────
// MetadataService — Spotify metadata and search, carved out of the former App
// god-object (R3). Streaming-link resolution and track-availability lookups also
// lived here until item 7b removed them with the rest of Song.link.
//
// Holds nothing. The AuthManager it used to carry was there for one purpose —
// resolving the SpotFetch fallback URL for the calling user — and went when that
// fallback did.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
)

// The JobManager dependency went with them: it was held only to borrow the
// JobManager's shared ISRC client.
type MetadataService struct{}

func NewMetadataService() *MetadataService {
	return &MetadataService{}
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
//
// Existait pour l'API externe SpotFetch, qui recevait l'URL telle quelle. Cette
// API n'est plus appelée, et le parseur natif (backend/spotify.parseSpotifyURI)
// couvre déjà les deux cas : il saute le segment intl-, et il lit Path, donc la
// query ne l'atteint jamais. Ce qui reste ici est donc une ceinture par-dessus
// des bretelles — sans effet mesurable, testé, gardé faute d'une raison de le
// retirer, pas faute d'en avoir besoin.
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

// GetSpotifyMetadata fetches track/album/playlist/artist metadata through the
// native TOTP client.
//
// A second source used to sit behind it: a SpotFetch-compatible API, tried
// whenever the native call failed, at a URL resolved per user. It is gone. The
// shipped default was a third party that has never once answered — the fallback
// never fired on any deployment — and upstream dropped the setting as well. The
// escape hatch it was meant to be is worth rebuilding the day the native path
// breaks; keeping a dead one wired in was not.
func (m *MetadataService) GetSpotifyMetadata(req SpotifyMetadataRequest) (string, error) {
	if req.URL == "" {
		return "", fmt.Errorf("URL parameter is required")
	}
	// Normaliser l'URL : supprimer intl-xx/ et ?si=... (redondant, voir plus haut)
	req.URL = normalizeSpotifyURL(req.URL)
	if req.Delay == 0 {
		req.Delay = 1.0
	}
	if req.Timeout == 0 {
		req.Timeout = 300.0
	}

	metaCtx, metaCancel := context.WithTimeout(context.Background(), time.Duration(req.Timeout*float64(time.Second)))
	defer metaCancel()

	// Client natif Spotify (TOTP)
	data, err := spotify.GetFilteredSpotifyData(metaCtx, req.URL, req.Batch, time.Duration(req.Delay*float64(time.Second)))
	if err != nil {
		return "", fmt.Errorf("failed to fetch metadata: %v", err)
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
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
