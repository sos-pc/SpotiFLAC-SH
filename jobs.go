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
	"database/sql"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/isrclookup"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	jobWorkers = 1         // parallel download workers
	dbFile     = "jobs.db" // path relative to configDir
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
	// M3U8Name / M3U8SourceID ask for an M3U8 to be written when this batch
	// finishes. Only manual batches set them: a watchlist's M3U8 is the
	// watcher's job. The name is the playlist/album label, the source id
	// disambiguates the filename the way a watchlist id does.
	M3U8Name     string `json:"m3u8_name,omitempty"`
	M3U8SourceID string `json:"m3u8_source_id,omitempty"`
}

// BatchM3U8Request is what a finished manual batch needs to name and place its
// M3U8. Carried in memory only: after a restart the batch counters are gone
// too, so a batch spanning a restart writes no M3U8 — same degradation the
// watchlist counters already accept.
type BatchM3U8Request struct {
	Name     string
	SourceID string
	UserID   string
	// Tracks the catalog dedup recognised as already downloaded, so no job was
	// ever created for them. Without this the M3U8 would list only what THIS
	// batch downloaded — "the last batch" rather than "the playlist" — because
	// a deduped track never reaches a worker and so never becomes a skipped job
	// with a path.
	Preexisting []M3U8Entry
}

// M3U8Entry is one line of a generated playlist, kept with its position so the
// final file follows playlist order rather than completion order.
type M3U8Entry struct {
	Position int
	Path     string
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
	// OnManualBatchComplete is called when a NON-watchlist batch that asked
	// for an M3U8 has finished, with the file paths that actually landed on
	// disk, in playlist order. Implemented by the watcher because writing the
	// file needs the user's settings, which the job manager has no access to.
	OnManualBatchComplete(req BatchM3U8Request, paths []string)
}

// ─────────────────────────────────────────────────────────────────────────────
// JobManager
// ─────────────────────────────────────────────────────────────────────────────

type JobManager struct {
	db           *bolt.DB
	catalog      *sql.DB // SQLite catalog: tracks, library_files, download_attempts
	queue        chan string
	isrcClient   *isrclookup.Client
	eventHandler JobEventHandler
	hub          *SSEHub
	wg           sync.WaitGroup
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.RWMutex
	closedOnce   sync.Once
	// In-memory batch counters for O(1) batch-completion detection.
	// Protected by mu.
	batchTotals  map[string]int    // batchID → total jobs enqueued
	batchDone    map[string]int    // batchID → terminal jobs count
	batchWatchID map[string]string // batchID → watchlistID
	// batchID → what a MANUAL (non-watchlist) batch needs to write its M3U8
	// once every job has finished. Watchlists are absent from this map: the
	// watcher owns their generation.
	batchM3U8 map[string]BatchM3U8Request
	// Injected by NewWatcher to retrieve current watchlist settings at runtime.
	getWatchlistSettings func(watchlistID string) (JobSettings, bool)
}

// forgetBatch drops every in-memory trace of a batch, in one place so a new
// per-batch map cannot be added to EnqueueBatch and forgotten here.
//
// Note: clearing the queue does NOT strand these — ClearAllJobs refuses to
// delete pending or downloading jobs, so an in-flight batch always reaches its
// own completion.
func (jm *JobManager) forgetBatch(batchID string) {
	delete(jm.batchTotals, batchID)
	delete(jm.batchDone, batchID)
	delete(jm.batchWatchID, batchID)
	delete(jm.batchM3U8, batchID)
}

// SetEventHandler connects the event handler (typically *Watcher).
// Must be called before the first EnqueueBatch.
func (jm *JobManager) SetEventHandler(h JobEventHandler) {
	jm.eventHandler = h
}

// NewJobManager opens BoltDB, creates buckets, starts workers and the
// periodic cleanup goroutine. catalog is the SQLite handle returned by
// backend/db.Open; the manager records every terminal transition there
// (best-effort, errors logged not propagated).
func NewJobManager(configDir string, db *bolt.DB, catalog *sql.DB) (*JobManager, error) {
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
		db:           db,
		catalog:      catalog,
		queue:        make(chan string, 10000),
		isrcClient:   isrclookup.Shared(),
		hub:          newSSEHub(),
		ctx:          ctx,
		cancel:       cancel,
		batchTotals:  make(map[string]int),
		batchDone:    make(map[string]int),
		batchWatchID: make(map[string]string),
		batchM3U8:    make(map[string]BatchM3U8Request),
	}

	jm.recoverPendingJobs()

	for i := 0; i < jobWorkers; i++ {
		jm.wg.Add(1)
		workerID := i
		util.SafeGo(fmt.Sprintf("jobs.worker[%d]", workerID), func() { jm.worker(workerID) })
	}

	util.SafeGo("jobs.cleanupLoop", jm.cleanupLoop)

	slog.Info("[Jobs] Manager started", "workers", jobWorkers, "db", filepath.Join(configDir, dbFile))
	return jm, nil
}

// notifyJob publishes a job_update event to all connected SSE clients.
func (jm *JobManager) notifyJob(job *Job) {
	if jm.hub != nil {
		jm.hub.publish(JobEvent{Type: "job_update", Job: job})
	}
}

// cleanupLoop runs CleanupOldJobs after 5 minutes, then every 24 h. Each
// run is wrapped with runCleanupSafely so a panic inside CleanupOldJobs
// only skips that one run instead of permanently killing this goroutine —
// without per-run recovery, an unrecovered panic anywhere in the loop body
// would silently stop cleanup forever (nothing restarts it) rather than
// just failing once and retrying on the next tick.
func (jm *JobManager) cleanupLoop() {
	select {
	case <-time.After(5 * time.Minute):
	case <-jm.ctx.Done():
		return
	}
	jm.runCleanupSafely()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-jm.ctx.Done():
			return
		case <-ticker.C:
			jm.runCleanupSafely()
		}
	}
}

func (jm *JobManager) runCleanupSafely() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[Jobs] PANIC recovered in jobs cleanup", "recover", r, "stack", string(debug.Stack()))
		}
	}()
	if deleted, _, err := jm.CleanupOldJobs(); err == nil && deleted > 0 {
		slog.Info("[Jobs] Cleanup: old jobs deleted", "count", deleted)
	}
}

// Close shuts down workers gracefully.
// closedOnce ensures the channel is never closed twice.
func (jm *JobManager) Close() {
	jm.closedOnce.Do(func() {
		slog.Info("[Jobs] Shutting down...")
		jm.cancel()
		close(jm.queue)
		jm.wg.Wait()
		slog.Info("[Jobs] Shutdown complete")
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// EnqueueBatch — called from the HTTP API
// ─────────────────────────────────────────────────────────────────────────────

func (jm *JobManager) EnqueueBatch(req EnqueueBatchRequest) (EnqueueBatchResponse, error) {
	if len(req.Tracks) == 0 {
		return EnqueueBatchResponse{}, fmt.Errorf("no tracks provided")
	}

	// The dedup check below reads a snapshot of existing jobs, then loops
	// over req.Tracks deciding what to insert against that snapshot — a
	// classic TOCTOU race if two EnqueueBatch calls run concurrently (a
	// watchlist's own scheduled sync firing at the same time as a manual
	// "Sync" click, or two syncs racing on restart): both would read the
	// same pre-insert snapshot, neither would see the other's inserts, and
	// both would enqueue a duplicate job for the same track+watchlist. jm.mu
	// already exists for the batch-counter bookkeeping a few lines down;
	// holding it for this whole call serializes EnqueueBatch calls against
	// each other so the snapshot a call reads is always up to date with
	// every insert any prior call completed, closing that race. The extra
	// contention this adds is only against other EnqueueBatch calls and the
	// brief batch-counter update in maybeGenerateM3U8 — not against the
	// worker's actual download work, which never holds this lock.
	jm.mu.Lock()
	defer jm.mu.Unlock()

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
	// A manual batch that asked for an M3U8 must end up listing the whole
	// playlist, not just what this run downloaded — so remember the tracks the
	// catalog dedup takes out below, which never become jobs.
	wantsM3U8 := req.WatchlistID == "" && req.M3U8Name != ""
	var preexisting []M3U8Entry

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

		// Catalog-based dedup: if we already have this track at equal-or-
		// better quality and the file is still on disk, skip the enqueue
		// entirely. Records a skipped DownloadAttempt so the audit trail
		// shows the track was considered.
		if dedup := jm.checkCatalogDedup(track.SpotifyID, req.Settings); dedup.skip {
			slog.Info("[Jobs] Catalog dedup", "track", track.TrackName, "reason", dedup.reason)
			jm.recordCatalogDedupSkip(track, req, dedup.libraryFileID, dedup.filePath, dedup.reason)
			if wantsM3U8 && dedup.filePath != "" {
				preexisting = append(preexisting, M3U8Entry{Position: track.Position, Path: dedup.filePath})
			}
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
			slog.Error("[Jobs] Failed to persist job", "job_id", job.ID, "err", err)
			skipped++
			continue
		}
		jm.notifyJob(job)

		select {
		case jm.queue <- job.ID:
			enqueued++
		default:
			slog.Warn("[Jobs] Queue full, job will be picked up on next poll", "job_id", job.ID)
			enqueued++
		}
	}

	// Counters are registered for MANUAL batches too, not just watchlists:
	// without them maybeGenerateM3U8 could never detect that a manual batch had
	// finished, which is why a freshly-downloaded playlist never got an M3U8.
	if enqueued > 0 {
		// Already holding jm.mu for the whole call (see the top of this
		// function) — no separate lock/unlock needed here anymore.
		jm.batchTotals[batchID] = enqueued
		jm.batchDone[batchID] = 0
		jm.batchWatchID[batchID] = req.WatchlistID
		if wantsM3U8 {
			jm.batchM3U8[batchID] = BatchM3U8Request{
				Name:        req.M3U8Name,
				SourceID:    req.M3U8SourceID,
				UserID:      req.UserID,
				Preexisting: preexisting,
			}
		}
	} else if enqueued == 0 && wantsM3U8 && len(preexisting) > 0 && jm.eventHandler != nil {
		// Every track was already downloaded, so no job will ever fire the
		// completion hook — yet this is exactly the "regenerate the playlist
		// file" case, and it has all the paths it needs right here.
		//
		// Deferred, NOT called inline: this whole function holds jm.mu, and the
		// handler writes an M3U8 to disk. Doing that under the lock would block
		// every other EnqueueBatch and every batch-counter update for the length
		// of a filesystem write, breaking the "brief" contention this lock's own
		// doc comment promises.
		sort.Slice(preexisting, func(a, b int) bool { return preexisting[a].Position < preexisting[b].Position })
		paths := make([]string, 0, len(preexisting))
		for _, e := range preexisting {
			paths = append(paths, e.Path)
		}
		m3u8Req := BatchM3U8Request{Name: req.M3U8Name, SourceID: req.M3U8SourceID, UserID: req.UserID}
		defer func() { jm.eventHandler.OnManualBatchComplete(m3u8Req, paths) }()
	} else if enqueued == 0 && skipped > 0 && req.WatchlistID != "" && jm.eventHandler != nil {
		// Every track in the batch was caught by dedup (duplicate active
		// job or catalog dedup) — no job was ever created with this
		// batchID, so maybeGenerateM3U8/OnBatchComplete (triggered by a
		// job reaching a terminal state) would otherwise never fire for
		// it. Without this, the watchlist's SyncLog permanently shows
		// Downloaded:0, Skipped:0, Failed:0 for a sync that in fact
		// matched N tracks via the catalog, and the M3U8 doesn't get a
		// chance to regenerate from this trigger either.
		jm.eventHandler.OnBatchComplete(req.WatchlistID, batchID, 0, skipped, 0)
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
