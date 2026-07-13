package main

// ─────────────────────────────────────────────────────────────────────────────
// MetadataService — Spotify metadata, search, streaming-link resolution and
// track-availability lookups carved out of the former App god-object (R3).
//
// Holds only its real dependencies: the JobManager (for its shared songlink
// client) and SystemService (to read the saved SpotFetch API URL fallback).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
	"github.com/afkarxyz/SpotiFLAC/backend/tidal"
)

type MetadataService struct {
	jobs   *JobManager
	system *SystemService
}

func NewMetadataService(jobs *JobManager, system *SystemService) *MetadataService {
	return &MetadataService{jobs: jobs, system: system}
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

func (m *MetadataService) GetStreamingURLs(spotifyTrackID string, region string) (string, error) {
	if spotifyTrackID == "" {
		return "", fmt.Errorf("spotify track ID is required")
	}
	slog.Debug("[GetStreamingURLs] Called", "track_id", spotifyTrackID, "region", region)
	jm := m.jobs
	if jm == nil {
		return "", fmt.Errorf("job manager not initialized")
	}
	urls, err := jm.songLinkClient.GetAllURLsFromSpotify(spotifyTrackID, region)

	// Si Songlink échoue ou ne trouve rien (ex: 429), on tente une recherche directe sur l'API Tidal
	if err != nil || urls == nil || (urls.TidalURL == "" && urls.AmazonURL == "") {
		slog.Debug("[GetStreamingURLs] Songlink failed/empty, falling back to direct Tidal Search", "err", err, "track_id", spotifyTrackID)

		// 1. Récupérer le nom de la piste et de l'artiste depuis Spotify via l'ID
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyTrackID)
		trackData, sErr := spotify.GetFilteredSpotifyData(ctx, trackURL, false, 0)

		if sErr == nil {
			var trackResp struct {
				Track struct {
					Name    string `json:"name"`
					Artists []struct {
						Name string `json:"name"`
					} `json:"artists"`
				} `json:"track"`
			}

			if jsonData, jsonErr := json.Marshal(trackData); jsonErr == nil {
				if json.Unmarshal(jsonData, &trackResp) == nil && trackResp.Track.Name != "" && len(trackResp.Track.Artists) > 0 {
					artistName := trackResp.Track.Artists[0].Name
					trackName := trackResp.Track.Name

					// 2. Lancer la recherche sur l'API Tidal
					dl := tidal.NewTidalDownloader("")
					if tidalURL, tErr := dl.SearchTidalByName(trackName, artistName); tErr == nil && tidalURL != "" {
						if urls == nil {
							urls = &songlink.SongLinkURLs{}
						}
						urls.TidalURL = tidalURL
						slog.Debug("[GetStreamingURLs] Fallback successful, found Tidal URL", "url", tidalURL)
						err = nil // On efface l'erreur Songlink pour le frontend
					} else {
						slog.Debug("[GetStreamingURLs] Tidal direct search failed", "err", tErr)
					}
				}
			}
		}
	}

	if err != nil {
		return "", err
	}
	jsonData, err := json.Marshal(urls)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
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

func (m *MetadataService) GetSpotifyMetadata(req SpotifyMetadataRequest) (string, error) {
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

	var spotFetchAPIURL string
	settings, err := m.system.LoadSettings()
	if err == nil && settings != nil {
		if apiURL, ok := settings["spotFetchAPIUrl"].(string); ok {
			spotFetchAPIURL = apiURL
		}
	}

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

func (m *MetadataService) CheckTrackAvailability(spotifyTrackID string) (string, error) {
	if spotifyTrackID == "" {
		return "", fmt.Errorf("spotify track ID is required")
	}
	jm := m.jobs
	if jm == nil {
		return "", fmt.Errorf("job manager not initialized")
	}
	availability, err := jm.songLinkClient.CheckTrackAvailability(spotifyTrackID)
	if err != nil {
		return "", err
	}
	jsonData, err := json.Marshal(availability)
	if err != nil {
		return "", fmt.Errorf("failed to encode response: %v", err)
	}
	return string(jsonData), nil
}

func (m *MetadataService) GetPreviewURL(trackID string) (string, error) {
	return spotify.GetPreviewURL(trackID)
}
