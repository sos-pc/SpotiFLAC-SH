package main

// ─────────────────────────────────────────────────────────────────────────────
// Worker loop and job processing for JobManager
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend"
	"github.com/sos-pc/SpotiFLAC-SH/backend/audio"
	bolt "go.etcd.io/bbolt"
)

func (jm *JobManager) worker(id int) {
	defer jm.wg.Done()
	slog.Info("[Jobs] Worker started", "worker_id", id)

	for {
		select {
		case <-jm.ctx.Done():
			slog.Info("[Jobs] Worker stopped", "worker_id", id)
			return
		case jobID, ok := <-jm.queue:
			if !ok {
				slog.Info("[Jobs] Worker queue closed", "worker_id", id)
				return
			}
			jm.processJobSafely(jobID)
		}
	}
}

// processJobSafely runs processJob with panic protection scoped to a
// single job, not the whole worker goroutine. jobWorkers is 1 by default
// (see its doc comment) — recovering only at the top of worker() would
// still keep the process alive, but the panic would permanently kill the
// sole worker goroutine (nothing restarts it), silently stalling the
// entire download queue forever with no crash and no obvious symptom.
// Recovering per job instead lets the worker loop continue, and mirrors
// processJob's own failure-path bookkeeping (status, catalog, batch
// completion) so a panicking job behaves like any other failed job to the
// rest of the system instead of getting stuck in Downloading forever.
func (jm *JobManager) processJobSafely(jobID string) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		slog.Error("[Jobs] PANIC recovered while processing job", "job_id", jobID, "recover", r, "stack", string(debug.Stack()))

		job, err := jm.loadJob(jobID)
		if err != nil {
			return
		}
		job.Status = StatusFailed
		job.Error = fmt.Sprintf("internal error: %v", r)
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		jm.notifyJob(job)
		jm.recordCatalogFailed(job)
		if job.WatchlistID != "" && job.SpotifyID != "" && jm.eventHandler != nil && isPermanentFailure(job.Error) {
			jm.eventHandler.OnPermanentFailure(job.WatchlistID, job.SpotifyID)
		}
		// Unconditional: maybeGenerateM3U8 guards on batchID itself and routes by
		// watchlistID. Gating the CALL on WatchlistID != "" is what kept manual
		// batches from ever having a completion moment.
		jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
	}()
	jm.processJob(jobID)
}

func (jm *JobManager) processJob(jobID string) {
	job, err := jm.loadJob(jobID)
	if err != nil {
		slog.Error("[Jobs] Failed to load job", "job_id", jobID, "err", err)
		return
	}

	if job.Status == StatusDone || job.Status == StatusSkipped {
		return
	}

	// For watchlist jobs, always use current watchlist settings so that
	// jobs created with stale settings (e.g. empty folderTemplate) still
	// download to the right location.
	if job.WatchlistID != "" && jm.getWatchlistSettings != nil {
		if freshSettings, ok := jm.getWatchlistSettings(job.WatchlistID); ok {
			job.Settings = freshSettings
		}
	}

	slog.Info("[Jobs] Processing", "track", job.TrackName, "artist", job.ArtistName)

	job.Status = StatusDownloading
	job.UpdatedAt = time.Now()
	job.StartedAt = time.Now()
	jm.saveJob(job)
	jm.notifyJob(job)

	outputDir := jm.buildOutputDir(job)

	if existingPath := jm.checkFileExists(job, outputDir); existingPath != "" {
		slog.Info("[Jobs] Already exists", "path", existingPath)
		job.Status = StatusSkipped
		job.FilePath = existingPath
		if info, err := os.Stat(existingPath); err == nil {
			job.TotalSize = float64(info.Size()) / 1024 / 1024
		}
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		jm.notifyJob(job)
		jm.recordCatalogSkipped(job)
		jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
		return
	}

	isrc := jm.resolveTrackISRC(job)

	req := jm.buildDownloadRequest(job, outputDir, isrc)
	lastPersisted := time.Now()
	req.SpeedCallback = func(mbDownloaded, speedMBps float64) {
		job.Speed = speedMBps
		job.TotalSize = mbDownloaded
		job.UpdatedAt = time.Now()
		// Persisted at most once every 2s (this callback itself already
		// fires roughly every 256KB — see ProgressWriter.Write — which for
		// a fast connection can be many times per second) so a client that
		// (re)connects mid-download — a page refresh, a tab coming back
		// from the background, a brief network drop — sees near-live
		// progress in its initial snapshot (v1JobsStream reads this same
		// BoltDB record) instead of the zero values this job was saved
		// with at the very start of the download, before any bytes had
		// moved.
		if time.Since(lastPersisted) >= 2*time.Second {
			jm.saveJob(job)
			lastPersisted = time.Now()
		}
		jm.notifyJob(job)
	}

	resp, err := backend.ExecuteDownload(req)
	if err != nil || !resp.Success {
		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		} else {
			errMsg = resp.Error
		}
		slog.Warn("[Jobs] Failed", "track", job.TrackName, "err", errMsg)
		job.Status = StatusFailed
		job.Error = errMsg
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		jm.notifyJob(job)
		jm.recordCatalogFailed(job)
		if job.WatchlistID != "" && job.SpotifyID != "" && jm.eventHandler != nil {
			if isPermanentFailure(errMsg) {
				jm.eventHandler.OnPermanentFailure(job.WatchlistID, job.SpotifyID)
			}
		}
		jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
		return
	}

	// A download that "succeeded" can still be the wrong audio: a community
	// Tidal proxy without a Premium token serves a ~30s preview and reports
	// success. Nothing downstream would notice — the file exists, has tags, and
	// looks right in a listing — so the library fills with junk silently.
	// Checked here, at the one point where a job becomes Done
	// (docs/upstream-catchup.md §S2).
	// resp.AlreadyExists means ExecuteDownload found the file already on disk and
	// downloaded nothing. Validating it would risk DELETING a file this job never
	// created — a legitimate extended mix or live version whose duration differs
	// from Spotify's is not a preview, and it is not ours to remove.
	if resp.File != "" && !resp.AlreadyExists && job.DurationMs > 0 {
		if err := audio.ValidateTrackDuration(resp.File, job.DurationMs/1000); err != nil {
			slog.Warn("[Jobs] Rejected download", "track", job.TrackName, "err", err)
			if rmErr := os.Remove(resp.File); rmErr != nil && !os.IsNotExist(rmErr) {
				slog.Error("[Jobs] Could not remove rejected file", "path", resp.File, "err", rmErr)
			}
			job.Status = StatusFailed
			job.Error = err.Error()
			job.Speed = 0
			job.UpdatedAt = time.Now()
			jm.saveJob(job)
			jm.notifyJob(job)
			jm.recordCatalogFailed(job)
			if job.WatchlistID != "" && job.SpotifyID != "" && jm.eventHandler != nil &&
				isPermanentFailure(job.Error) {
				jm.eventHandler.OnPermanentFailure(job.WatchlistID, job.SpotifyID)
			}
			jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
			return
		}
	}

	job.Status = StatusDone
	job.Speed = 0
	job.FilePath = resp.File
	job.Progress = 1.0
	if resp.File != "" {
		if info, err := os.Stat(resp.File); err == nil {
			job.TotalSize = float64(info.Size()) / 1024 / 1024
		}
	}
	job.UpdatedAt = time.Now()
	jm.saveJob(job)
	jm.notifyJob(job)
	jm.recordCatalogDone(job)
	slog.Info("[Jobs] Done", "track", job.TrackName)

	jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
}

// isPermanentFailure returns true when the error message does not match any
// known transient condition (rate-limit, timeout, network blip, third-party
// service outage). Permanent failures are reported to the event handler so
// the watcher does not retry them on the next sync cycle.
func isPermanentFailure(errMsg string) bool {
	transientPatterns := []string{
		"429", "rate limit", "timeout", "connection refused",
		"context deadline", "no such host", "dial tcp",
		"yoinkify", "deezmate", "lookup",
	}
	lower := strings.ToLower(errMsg)
	for _, p := range transientPatterns {
		if strings.Contains(lower, p) {
			return false
		}
	}
	return true
}

// recoverPendingJobs re-enqueues jobs that were interrupted at last shutdown.
func (jm *JobManager) recoverPendingJobs() {
	recovered := 0
	var toRecover []Job
	jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			if job.Status == StatusPending || job.Status == StatusDownloading {
				job.Status = StatusPending
				job.Progress = 0
				job.UpdatedAt = time.Now()
				toRecover = append(toRecover, job)
			}
			return nil
		})
	})
	for _, job := range toRecover {
		jobCopy := job
		jm.saveJob(&jobCopy)
		select {
		case jm.queue <- jobCopy.ID:
			recovered++
		default:
		}
	}
	if recovered > 0 {
		slog.Info("[Jobs] Recovered interrupted jobs", "count", recovered)
	}
}
