package main

// ─────────────────────────────────────────────────────────────────────────────
// Jobs — types, constants, JobManager lifecycle
//
// BoltDB persistence  → jobs_storage.go
// Worker / processing → jobs_worker.go
// Business helpers    → jobs_helpers.go
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	jobWorkers    = 1         // parallel download workers
	songLinkDelay = 6500      // ms between song.link requests (max 9/min)
	dbFile        = "jobs.db" // path relative to configDir
)

var (
	bucketJobs      = []byte("jobs")
	bucketWatchlist = []byte("watchlist")
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type JobStatus string

const (
	StatusPending     JobStatus = "pending"
	StatusDownloading JobStatus = "downloading"
	StatusDone        JobStatus = "done"
	StatusFailed      JobStatus = "failed"
	StatusSkipped     JobStatus = "skipped"
)

// JobSettings holds all download parameters sent by the frontend.
type JobSettings struct {
	Service              string `json:"service"`
	DownloadPath         string `json:"downloadPath"`
	FilenameTemplate     string `json:"filenameTemplate"`
	FolderTemplate       string `json:"folderTemplate"`
	TrackNumber          bool   `json:"trackNumber"`
	EmbedLyrics          bool   `json:"embedLyrics"`
	EmbedMaxQualityCover bool   `json:"embedMaxQualityCover"`
	TidalQuality         string `json:"tidalQuality"`
	QobuzQuality         string `json:"qobuzQuality"`
	AutoOrder            string `json:"autoOrder"`
	AutoQuality          string `json:"autoQuality"`
	UseFirstArtistOnly   bool   `json:"useFirstArtistOnly"`
	UseSingleGenre       bool   `json:"useSingleGenre"`
	EmbedGenre           bool   `json:"embedGenre"`
	CreatePlaylistFolder bool   `json:"createPlaylistFolder"`
	AllowFallback        bool   `json:"allowFallback"`
	Region               string `json:"region"`
}

// Job represents a single download persisted in BoltDB.
type Job struct {
	ID           string      `json:"id"`
	SpotifyID    string      `json:"spotify_id"`
	TrackName    string      `json:"track_name"`
	ArtistName   string      `json:"artist_name"`
	AlbumName    string      `json:"album_name"`
	AlbumArtist  string      `json:"album_artist"`
	ReleaseDate  string      `json:"release_date"`
	CoverURL     string      `json:"cover_url"`
	TrackNumber  int         `json:"track_number"`
	DiscNumber   int         `json:"disc_number"`
	TotalTracks  int         `json:"total_tracks"`
	TotalDiscs   int         `json:"total_discs"`
	Copyright    string      `json:"copyright"`
	Publisher    string      `json:"publisher"`
	Position     int         `json:"position"`
	PlaylistName string      `json:"playlist_name"`
	DurationMs   int         `json:"duration_ms"`
	Settings     JobSettings `json:"settings"`
	WatchlistID  string      `json:"watchlist_id,omitempty"`
	BatchID      string      `json:"batch_id,omitempty"`
	UserID       string      `json:"user_id,omitempty"`
	Status       JobStatus   `json:"status"`
	FilePath     string      `json:"file_path,omitempty"`
	TotalSize    float64     `json:"total_size,omitempty"`
	Speed        float64     `json:"speed,omitempty"`
	Progress     float64     `json:"progress,omitempty"`
	Error        string      `json:"error,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
	StartedAt    time.Time   `json:"started_at"`
}

// EnqueueBatchRequest is received from the HTTP API.
type EnqueueBatchRequest struct {
	Tracks      []JobTrack  `json:"tracks"`
	Settings    JobSettings `json:"settings"`
	WatchlistID string      `json:"watchlist_id,omitempty"`
	UserID      string      `json:"user_id,omitempty"`
}

// JobTrack is the per-track payload inside EnqueueBatchRequest.
type JobTrack struct {
	SpotifyID    string `json:"spotify_id"`
	TrackName    string `json:"track_name"`
	ArtistName   string `json:"artist_name"`
	AlbumName    string `json:"album_name"`
	AlbumArtist  string `json:"album_artist"`
	ReleaseDate  string `json:"release_date"`
	CoverURL     string `json:"cover_url"`
	TrackNumber  int    `json:"track_number"`
	DiscNumber   int    `json:"disc_number"`
	TotalTracks  int    `json:"total_tracks"`
	TotalDiscs   int    `json:"total_discs"`
	Copyright    string `json:"copyright"`
	Publisher    string `json:"publisher"`
	Position     int    `json:"position"`
	PlaylistName string `json:"playlist_name"`
	DurationMs   int    `json:"duration_ms"`
}

type EnqueueBatchResponse struct {
	Enqueued int    `json:"enqueued"`
	Skipped  int    `json:"skipped"`
	Message  string `json:"message"`
	BatchID  string `json:"batch_id,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// JobEventHandler — interface implemented by Watcher to break the
// jobs↔watcher circular dependency.
// ─────────────────────────────────────────────────────────────────────────────

type JobEventHandler interface {
	// OnPermanentFailure is called when a job fails permanently (not a
	// transient error) so that the watcher does not retry it on the next sync.
	OnPermanentFailure(watchlistID, spotifyID string)
	// OnBatchComplete is called when all jobs in a batch have reached a
	// terminal state. Updates the SyncLog and generates the M3U8 file.
	OnBatchComplete(watchlistID, batchID string, downloaded, skipped, failed int)
}

// ─────────────────────────────────────────────────────────────────────────────
// JobManager
// ─────────────────────────────────────────────────────────────────────────────

type JobManager struct {
	db             *bolt.DB
	queue          chan string
	songLinkSem    chan struct{}
	songLinkClient *songlink.SongLinkClient
	eventHandler   JobEventHandler
	hub            *SSEHub
	wg             sync.WaitGroup
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.RWMutex
	closedOnce     sync.Once
	// In-memory batch counters for O(1) batch-completion detection.
	// Protected by mu.
	batchTotals  map[string]int    // batchID → total jobs enqueued
	batchDone    map[string]int    // batchID → terminal jobs count
	batchWatchID map[string]string // batchID → watchlistID
	// Injected by NewWatcher to retrieve current watchlist settings at runtime.
	getWatchlistSettings func(watchlistID string) (JobSettings, bool)
}

// SetEventHandler connects the event handler (typically *Watcher).
// Must be called before the first EnqueueBatch.
func (jm *JobManager) SetEventHandler(h JobEventHandler) {
	jm.eventHandler = h
}

// NewJobManager opens BoltDB, creates buckets, starts workers and the
// periodic cleanup goroutine.
func NewJobManager(configDir string, db *bolt.DB) (*JobManager, error) {
	err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketJobs); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(bucketWatchlist); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init DB buckets: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	jm := &JobManager{
		db:             db,
		queue:          make(chan string, 10000),
		songLinkSem:    make(chan struct{}, 1),
		songLinkClient: songlink.GetSongLinkClient(),
		hub:            newSSEHub(),
		ctx:            ctx,
		cancel:         cancel,
		batchTotals:    make(map[string]int),
		batchDone:      make(map[string]int),
		batchWatchID:   make(map[string]string),
	}

	jm.recoverPendingJobs()

	for i := 0; i < jobWorkers; i++ {
		jm.wg.Add(1)
		go jm.worker(i)
	}

	go jm.cleanupLoop()

	fmt.Printf("[Jobs] Manager started (%d workers, db: %s)\n", jobWorkers, filepath.Join(configDir, dbFile))
	return jm, nil
}

// notifyJob publishes a job_update event to all connected SSE clients.
func (jm *JobManager) notifyJob(job *Job) {
	if jm.hub != nil {
		jm.hub.publish(JobEvent{Type: "job_update", Job: job})
	}
}

// cleanupLoop runs CleanupOldJobs after 5 minutes, then every 24 h.
func (jm *JobManager) cleanupLoop() {
	select {
	case <-time.After(5 * time.Minute):
	case <-jm.ctx.Done():
		return
	}
	if deleted, _, err := jm.CleanupOldJobs(); err == nil && deleted > 0 {
		fmt.Printf("[Jobs] Cleanup: %d old jobs deleted\n", deleted)
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-jm.ctx.Done():
			return
		case <-ticker.C:
			if deleted, _, err := jm.CleanupOldJobs(); err == nil && deleted > 0 {
				fmt.Printf("[Jobs] Cleanup: %d old jobs deleted\n", deleted)
			}
		}
	}
}

// Close shuts down workers gracefully.
// closedOnce ensures the channel is never closed twice.
func (jm *JobManager) Close() {
	jm.closedOnce.Do(func() {
		fmt.Println("[Jobs] Shutting down...")
		jm.cancel()
		close(jm.queue)
		jm.wg.Wait()
		fmt.Println("[Jobs] Shutdown complete")
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// EnqueueBatch — called from the HTTP API
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) EnqueueBatch(req EnqueueBatchRequest) (EnqueueBatchResponse, error) {
	if len(req.Tracks) == 0 {
		return EnqueueBatchResponse{}, fmt.Errorf("no tracks provided")
	}

	existingJobs, _ := jm.GetAllJobs()
	activeJobs := make(map[string]bool)
	for _, j := range existingJobs {
		if j.Status == StatusPending || j.Status == StatusDownloading {
			activeJobs[j.SpotifyID+"|"+j.WatchlistID] = true
		}
	}

	batchID := fmt.Sprintf("%s-%d", req.WatchlistID, time.Now().UnixNano())
	enqueued := 0
	skipped := 0

	for _, track := range req.Tracks {
		if track.SpotifyID == "" {
			skipped++
			continue
		}
		dupKey := track.SpotifyID + "|" + req.WatchlistID
		if activeJobs[dupKey] {
			skipped++
			continue
		}

		job := &Job{
			ID:           fmt.Sprintf("%s-%d", track.SpotifyID, time.Now().UnixNano()),
			SpotifyID:    track.SpotifyID,
			TrackName:    track.TrackName,
			ArtistName:   track.ArtistName,
			AlbumName:    track.AlbumName,
			AlbumArtist:  track.AlbumArtist,
			ReleaseDate:  track.ReleaseDate,
			CoverURL:     track.CoverURL,
			TrackNumber:  track.TrackNumber,
			DiscNumber:   track.DiscNumber,
			TotalTracks:  track.TotalTracks,
			TotalDiscs:   track.TotalDiscs,
			Copyright:    track.Copyright,
			Publisher:    track.Publisher,
			Position:     track.Position,
			PlaylistName: track.PlaylistName,
			DurationMs:   track.DurationMs,
			Settings:     req.Settings,
			WatchlistID:  req.WatchlistID,
			BatchID:      batchID,
			UserID:       req.UserID,
			Status:       StatusPending,
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		}

		if err := jm.saveJob(job); err != nil {
			fmt.Printf("[Jobs] Failed to persist job %s: %v\n", job.ID, err)
			skipped++
			continue
		}
		jm.notifyJob(job)

		select {
		case jm.queue <- job.ID:
			enqueued++
		default:
			fmt.Printf("[Jobs] Queue full, job %s will be picked up on next poll\n", job.ID)
			enqueued++
		}
	}

	if enqueued > 0 && req.WatchlistID != "" {
		jm.mu.Lock()
		jm.batchTotals[batchID] = enqueued
		jm.batchDone[batchID] = 0
		jm.batchWatchID[batchID] = req.WatchlistID
		jm.mu.Unlock()
	}

	return EnqueueBatchResponse{
		Enqueued: enqueued,
		Skipped:  skipped,
		Message:  fmt.Sprintf("%d tracks enqueued for background download", enqueued),
		BatchID:  batchID,
	}, nil
}

// defaultMusicPath is kept here to avoid importing util in jobs_helpers.go
// for just this one call (already imported there for other uses).
var _ = util.GetDefaultMusicPath // ensure util is reachable from this file
