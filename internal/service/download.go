package service

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

	"github.com/sos-pc/SpotiFLAC-SH/backend"
	"github.com/sos-pc/SpotiFLAC-SH/backend/spotify"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
)

type DownloadRequest = backend.DownloadRequest
type DownloadResponse = backend.DownloadResponse

type DownloadService struct {
	jobs *jobs.JobManager
	auth *auth.AuthManager
}

func NewDownloadService(jobs *jobs.JobManager, auth *auth.AuthManager) *DownloadService {
	return &DownloadService{jobs: jobs, auth: auth}
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
		return DownloadResponse{Success: false, Error: "jobs.JobManager not initialized"}, fmt.Errorf("job manager not initialized")
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
	serverSettings := settings.EffectiveDownloadSettings(d.auth, req.UserID)

	// Création du Job
	job := &jobs.Job{
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
		Status:       jobs.StatusPending,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
		// Fully server-authoritative (step 3): every download setting comes from
		// the user's server settings; the only per-download override is the
		// service. The request's quality/embed/path fields are ignored.
		Settings: settings.ServerJobSettings(serverSettings, req.Service),
	}

	// Persist and enqueue in one call — see JobManager.Submit for why this is
	// not two.
	queued, err := jm.Submit(job)
	if err != nil {
		// saveJob's failure used to be discarded here — the caller got
		// "Added to download queue" for a job that was never persisted.
		return DownloadResponse{}, fmt.Errorf("could not save job: %w", err)
	}
	if queued {
		slog.Info("[Download] jobs.Job added to queue", "job_id", job.ID)
	} else {
		slog.Warn("[Download] Queue full, job will be picked up later", "job_id", job.ID)
	}

	// Informe le frontend (qui gère l'état via l'historique/queue)
	return DownloadResponse{
		Success: true,
		Message: "Added to download queue",
		ItemID:  itemID,
	}, nil
}

func (d *DownloadService) EnqueueBatch(req jobs.EnqueueBatchRequest) (jobs.EnqueueBatchResponse, error) {
	jm := d.jobs
	if jm == nil {
		return jobs.EnqueueBatchResponse{}, fmt.Errorf("job manager not initialized")
	}
	return jm.EnqueueBatch(req)
}
