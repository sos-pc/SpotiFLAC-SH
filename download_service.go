package main

// ─────────────────────────────────────────────────────────────────────────────
// DownloadService — download enqueuing (single track + batch) and the
// settings-fallback logic REST callers rely on, carved out of the former App
// god-object (R3).
//
// Holds only its real dependencies: the JobManager (queue + persistence) and
// AuthManager (for EffectiveDownloadSettings' per-user lookup — see
// ApplySettingsFallbacks / DownloadSettings, R8).
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
	jobs *JobManager
	auth *AuthManager
}

func NewDownloadService(jobs *JobManager, auth *AuthManager) *DownloadService {
	return &DownloadService{jobs: jobs, auth: auth}
}

// ApplySettingsFallbacks fills zero-value fields in req with userID's
// effective settings (their own saved settings if any, else the operator's
// global config.json — see EffectiveDownloadSettings). Intended for REST API
// callers that send minimal payloads; the Wails frontend always provides all
// fields explicitly. userID == "" resolves to the global settings.
func (d *DownloadService) ApplySettingsFallbacks(req *DownloadRequest, userID string) {
	settings := EffectiveDownloadSettings(d.auth, userID)

	if req.OutputDir == "" {
		req.OutputDir = settings.DownloadPath
	}
	if req.FilenameFormat == "" {
		req.FilenameFormat = settings.FilenameTemplate
	}
	if req.AudioFormat == "" {
		switch req.Service {
		case "qobuz":
			req.AudioFormat = settings.QobuzQuality
		default:
			req.AudioFormat = settings.TidalQuality
		}
	}
	if req.AutoOrder == "" {
		req.AutoOrder = settings.AutoOrder
	}
	if !req.EmbedLyrics {
		req.EmbedLyrics = settings.EmbedLyrics
	}
	if !req.EmbedMaxQualityCover {
		req.EmbedMaxQualityCover = settings.EmbedMaxQualityCover
	}
	if !req.AllowFallback {
		req.AllowFallback = settings.AllowFallback
	}
	if !req.UseFirstArtistOnly {
		req.UseFirstArtistOnly = settings.UseFirstArtistOnly
	}
	if !req.UseSingleGenre {
		req.UseSingleGenre = settings.UseSingleGenre
	}
	if !req.EmbedGenre {
		req.EmbedGenre = settings.EmbedGenre
	}
	if !req.TrackNumber {
		req.TrackNumber = settings.TrackNumber
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

	// Backend-authoritative path/filename (docs/settings-source-of-truth.md,
	// step 1): the folder template, filename template and the flags that shape
	// the output path come from the user's saved server settings, not from the
	// request. buildOutputDir (in the worker) then applies the template on top
	// of the already-confined base path. Quality/embed flags still come from the
	// request for now — that's a later step of the same migration.
	serverSettings := EffectiveDownloadSettings(d.auth, req.UserID)

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
			Service: req.Service,
			// Path/filename: server-authoritative. DownloadPath is the confined
			// server base (handler dropped the client's output_dir); the template
			// is applied by buildOutputDir.
			DownloadPath:         req.OutputDir,
			FolderTemplate:       serverSettings.FolderTemplate,
			CreatePlaylistFolder: serverSettings.CreatePlaylistFolder,
			FilenameTemplate:     serverSettings.FilenameTemplate,
			TrackNumber:          serverSettings.TrackNumber,
			UseFirstArtistOnly:   serverSettings.UseFirstArtistOnly,
			// Quality/embed/fallback still from the request (later migration step).
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
			UseSingleGenre: req.UseSingleGenre,
			EmbedGenre:     req.EmbedGenre,
			AllowFallback:  req.AllowFallback,
			Region:         "", // Region is rarely used in manual download
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
