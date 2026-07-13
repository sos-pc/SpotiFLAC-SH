package main

// ─────────────────────────────────────────────────────────────────────────────
// DownloadService — download enqueuing (single track + batch) and the
// settings-fallback logic REST callers rely on, carved out of the former App
// god-object (R3).
//
// Holds only its real dependencies: the JobManager (queue + persistence) and
// SystemService (to read the saved global settings used as fallbacks).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/spotify"
)

type DownloadRequest = backend.DownloadRequest
type DownloadResponse = backend.DownloadResponse

type DownloadService struct {
	jobs   *JobManager
	system *SystemService
}

func NewDownloadService(jobs *JobManager, system *SystemService) *DownloadService {
	return &DownloadService{jobs: jobs, system: system}
}

// ApplySettingsFallbacks fills zero-value fields in req with the user's saved
// global settings. Intended for REST API callers that send minimal payloads;
// the Wails frontend always provides all fields explicitly.
func (d *DownloadService) ApplySettingsFallbacks(req *DownloadRequest) {
	settings, err := d.system.LoadSettings()
	if err != nil || settings == nil {
		return
	}
	getBool := func(key string) (bool, bool) {
		if v, ok := settings[key]; ok {
			if b, ok := v.(bool); ok {
				return b, true
			}
		}
		return false, false
	}
	getString := func(key string) string {
		if v, ok := settings[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	if req.OutputDir == "" {
		req.OutputDir = getString("downloadPath")
	}
	if req.FilenameFormat == "" {
		req.FilenameFormat = getString("filenameTemplate")
	}
	if req.AudioFormat == "" {
		switch req.Service {
		case "qobuz":
			req.AudioFormat = getString("qobuzQuality")
		default:
			req.AudioFormat = getString("tidalQuality")
		}
	}
	if req.AutoOrder == "" {
		req.AutoOrder = getString("autoOrder")
	}
	if !req.EmbedLyrics {
		if v, ok := getBool("embedLyrics"); ok {
			req.EmbedLyrics = v
		}
	}
	if !req.EmbedMaxQualityCover {
		if v, ok := getBool("embedMaxQualityCover"); ok {
			req.EmbedMaxQualityCover = v
		}
	}
	if !req.AllowFallback {
		if v, ok := getBool("allowFallback"); ok {
			req.AllowFallback = v
		}
	}
	if !req.UseFirstArtistOnly {
		if v, ok := getBool("useFirstArtistOnly"); ok {
			req.UseFirstArtistOnly = v
		}
	}
	if !req.UseSingleGenre {
		if v, ok := getBool("useSingleGenre"); ok {
			req.UseSingleGenre = v
		}
	}
	if !req.EmbedGenre {
		if v, ok := getBool("embedGenre"); ok {
			req.EmbedGenre = v
		}
	}
	if !req.TrackNumber {
		if v, ok := getBool("trackNumber"); ok {
			req.TrackNumber = v
		}
	}
}

func (d *DownloadService) DownloadTrack(req DownloadRequest) (DownloadResponse, error) {
	if req.Service == "" {
		req.Service = "auto"
	}

	itemID := req.ItemID
	if itemID == "" {
		if req.SpotifyID != "" {
			itemID = fmt.Sprintf("%s-%d", req.SpotifyID, time.Now().UnixNano())
		} else {
			itemID = fmt.Sprintf("%s-%s-%d", req.TrackName, req.ArtistName, time.Now().UnixNano())
		}
	}

	jm := d.jobs
	if jm == nil {
		return DownloadResponse{Success: false, Error: "JobManager not initialized"}, fmt.Errorf("job manager not initialized")
	}

	// Récupération des métadonnées Spotify manquantes (AlbumArtist, Duration, etc.) si possible
	if req.SpotifyID != "" && (req.AlbumArtist == "" || req.Duration == 0) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", req.SpotifyID)
		if trackData, err := spotify.GetFilteredSpotifyData(ctx, trackURL, false, 0); err == nil {
			var trackResp struct {
				Track struct {
					Album struct {
						Artists []struct {
							Name string `json:"name"`
						} `json:"artists"`
					} `json:"album"`
					DurationMs int `json:"duration_ms"`
				} `json:"track"`
			}
			jsonData, _ := json.Marshal(trackData)
			if json.Unmarshal(jsonData, &trackResp) == nil {
				if req.AlbumArtist == "" && len(trackResp.Track.Album.Artists) > 0 {
					req.AlbumArtist = trackResp.Track.Album.Artists[0].Name
				}
				if req.Duration == 0 && trackResp.Track.DurationMs > 0 {
					req.Duration = trackResp.Track.DurationMs / 1000
				}
			}
		}
	}

	// Création du Job
	job := &Job{
		ID:           itemID,
		SpotifyID:    req.SpotifyID,
		TrackName:    req.TrackName,
		ArtistName:   req.ArtistName,
		AlbumName:    req.AlbumName,
		AlbumArtist:  req.AlbumArtist,
		ReleaseDate:  req.ReleaseDate,
		CoverURL:     req.CoverURL,
		TrackNumber:  req.SpotifyTrackNumber,
		DiscNumber:   req.SpotifyDiscNumber,
		TotalTracks:  req.SpotifyTotalTracks,
		TotalDiscs:   req.SpotifyTotalDiscs,
		Copyright:    req.Copyright,
		Publisher:    req.Publisher,
		Position:     req.Position,
		PlaylistName: req.PlaylistName,
		DurationMs:   req.Duration * 1000,
		UserID:       req.UserID,
		Status:       StatusPending,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		Settings: JobSettings{
			Service:              req.Service,
			DownloadPath:         req.OutputDir,
			FilenameTemplate:     req.FilenameFormat,
			FolderTemplate:       "", // outputDir is pre-built by the frontend (folder template already applied)
			TrackNumber:          req.TrackNumber,
			EmbedLyrics:          req.EmbedLyrics,
			EmbedMaxQualityCover: req.EmbedMaxQualityCover,
			AutoOrder:            req.AutoOrder,
			TidalQuality:         backend.TidalQualityFor(req.AudioFormat),
			QobuzQuality:         backend.QobuzQualityFor(req.AudioFormat),
			AutoQuality: func() string {
				if req.AudioFormat == "HI_RES_LOSSLESS" || req.AudioFormat == "HI_RES" {
					return "24"
				}
				return ""
			}(),
			UseFirstArtistOnly: req.UseFirstArtistOnly,
			UseSingleGenre:     req.UseSingleGenre,
			EmbedGenre:         req.EmbedGenre,
			AllowFallback:      req.AllowFallback,
			Region:             "", // Region is rarely used in manual download
		},
	}

	// Ajout à la base de données et à la queue via les méthodes thread-safe de JobManager
	jm.saveJob(job)

	select {
	case jm.queue <- job.ID:
		slog.Info("[Download] Job added to queue", "job_id", job.ID)
	default:
		slog.Warn("[Download] Queue full, job will be picked up later", "job_id", job.ID)
	}

	// Informe le frontend (qui gère l'état via l'historique/queue)
	return DownloadResponse{
		Success: true,
		Message: "Added to download queue",
		ItemID:  itemID,
	}, nil
}

func (d *DownloadService) EnqueueBatch(req EnqueueBatchRequest) (EnqueueBatchResponse, error) {
	jm := d.jobs
	if jm == nil {
		return EnqueueBatchResponse{}, fmt.Errorf("job manager not initialized")
	}
	return jm.EnqueueBatch(req)
}
