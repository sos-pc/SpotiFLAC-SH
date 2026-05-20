package main

// ─────────────────────────────────────────────────────────────────────────────
// Worker loop and job processing for JobManager
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
	bolt "go.etcd.io/bbolt"
)

func (jm *JobManager) worker(id int) {
	defer jm.wg.Done()
	fmt.Printf("[Jobs] Worker %d started\n", id)

	for {
		select {
		case <-jm.ctx.Done():
			fmt.Printf("[Jobs] Worker %d stopped\n", id)
			return
		case jobID, ok := <-jm.queue:
			if !ok {
				fmt.Printf("[Jobs] Worker %d queue closed\n", id)
				return
			}
			jm.processJob(jobID)
		}
	}
}

func (jm *JobManager) processJob(jobID string) {
	job, err := jm.loadJob(jobID)
	if err != nil {
		fmt.Printf("[Jobs] Failed to load job %s: %v\n", jobID, err)
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

	fmt.Printf("[Jobs] Processing: %s - %s\n", job.TrackName, job.ArtistName)

	job.Status = StatusDownloading
	job.UpdatedAt = time.Now()
	job.StartedAt = time.Now()
	jm.saveJob(job)
	jm.notifyJob(job)

	outputDir := jm.buildOutputDir(job)

	if existingPath := jm.checkFileExists(job, outputDir); existingPath != "" {
		fmt.Printf("[Jobs] Already exists: %s\n", existingPath)
		job.Status = StatusSkipped
		job.FilePath = existingPath
		if info, err := os.Stat(existingPath); err == nil {
			job.TotalSize = float64(info.Size()) / 1024 / 1024
		}
		job.UpdatedAt = time.Now()
		jm.saveJob(job)
		jm.notifyJob(job)
		jm.recordCatalogSkipped(job)
		if job.WatchlistID != "" {
			jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
		}
		return
	}

	streamingURLs := jm.getStreamingURLs(job)

	req := jm.buildDownloadRequest(job, outputDir, streamingURLs)
	req.SpeedCallback = func(mbDownloaded, speedMBps float64) {
		job.Speed = speedMBps
		job.TotalSize = mbDownloaded
		job.UpdatedAt = time.Now()
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
		fmt.Printf("[Jobs] Failed: %s - %v\n", job.TrackName, errMsg)
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
		if job.WatchlistID != "" {
			jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
		}
		return
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
	fmt.Printf("[Jobs] Done: %s\n", job.TrackName)

	if job.WatchlistID != "" {
		jm.maybeGenerateM3U8(job.WatchlistID, job.BatchID)
	}
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
		fmt.Printf("[Jobs] Recovered %d interrupted jobs\n", recovered)
	}
}
