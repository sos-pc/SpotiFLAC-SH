package main

// ─────────────────────────────────────────────────────────────────────────────
// BoltDB persistence helpers for JobManager
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (jm *JobManager) saveJob(job *Job) error {
	return jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}
		data, err := json.Marshal(job)
		if err != nil {
			return err
		}
		return b.Put([]byte(job.ID), data)
	})
}

func (jm *JobManager) loadJob(id string) (*Job, error) {
	var job Job
	err := jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return fmt.Errorf("bucket not found")
		}
		data := b.Get([]byte(id))
		if data == nil {
			return fmt.Errorf("job not found: %s", id)
		}
		return json.Unmarshal(data, &job)
	})
	return &job, err
}

// GetJob returns a job by ID (read-only).
func (jm *JobManager) GetJob(id string) (Job, error) {
	job, err := jm.loadJob(id)
	if err != nil {
		return Job{}, err
	}
	return *job, nil
}

func (jm *JobManager) GetAllJobs() ([]Job, error) {
	var jobs []Job
	err := jm.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			jobs = append(jobs, job)
			return nil
		})
	})
	return jobs, err
}

// UpdateJobFilePathsForRename rewrites FilePath on every job currently
// pointing at oldPath to newPath. Renaming a file via File Manager only
// ever updated the SQLite catalog (syncCatalogPathOnRename) — BoltDB jobs
// kept the stale path, which meant recoverMissingFiles (watcher.go) saw
// the file as "missing" and redundantly re-queued it, and the
// playlist-track-removal path's os.Remove(job.FilePath) silently failed
// on the stale path, leaking the actual (renamed) file on disk forever.
// Called from syncCatalogPathOnRename so every rename path stays
// consistent without each downstream reader having to know about renames.
func (jm *JobManager) UpdateJobFilePathsForRename(oldPath, newPath string) (int, error) {
	var updated int
	err := jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toUpdate []struct {
			key []byte
			job Job
		}
		if err := b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err == nil && job.FilePath == oldPath {
				toUpdate = append(toUpdate, struct {
					key []byte
					job Job
				}{key: append([]byte(nil), k...), job: job})
			}
			return nil
		}); err != nil {
			return err
		}
		for _, u := range toUpdate {
			u.job.FilePath = newPath
			u.job.UpdatedAt = time.Now()
			data, err := json.Marshal(u.job)
			if err != nil {
				return err
			}
			if err := b.Put(u.key, data); err != nil {
				return err
			}
			updated++
		}
		return nil
	})
	return updated, err
}

// GetDoneFilesByWatchlist returns a map {spotifyID → filePath} for all done
// jobs of a given watchlist that have a recorded FilePath.
// Used to verify that "validated" files still exist on disk.
func (jm *JobManager) GetDoneFilesByWatchlist(watchlistID string) map[string]string {
	result := make(map[string]string)
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
			if job.WatchlistID == watchlistID && job.Status == StatusDone && job.FilePath != "" {
				result[job.SpotifyID] = job.FilePath
			}
			return nil
		})
	})
	return result
}

// CleanupOldJobs deduplicates jobs by (spotifyID, watchlistID), keeping the
// most relevant terminal status, and removes jobs older than 7 days.
func (jm *JobManager) CleanupOldJobs() (int, []string, error) {
	jobs, err := jm.GetAllJobs()
	if err != nil {
		return 0, nil, err
	}

	type key struct{ spotifyID, watchlistID string }
	latest := make(map[key]Job)
	noSpotifyID := []Job{}
	for _, j := range jobs {
		if j.SpotifyID == "" {
			noSpotifyID = append(noSpotifyID, j)
			continue
		}
		k := key{j.SpotifyID, j.WatchlistID}
		existing, ok := latest[k]
		if !ok {
			latest[k] = j
			continue
		}
		// Prefer Done over Failed even if Failed is more recent.
		if existing.Status == StatusDone && j.Status == StatusFailed {
			continue
		}
		if existing.Status == StatusFailed && j.Status == StatusDone {
			latest[k] = j
			continue
		}
		if j.UpdatedAt.After(existing.UpdatedAt) {
			latest[k] = j
		}
	}

	keepIDs := make(map[string]bool)
	for _, j := range latest {
		keepIDs[j.ID] = true
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, j := range noSpotifyID {
		if j.UpdatedAt.After(cutoff) {
			keepIDs[j.ID] = true
		}
	}
	for _, j := range jobs {
		if j.Status == StatusPending || j.Status == StatusDownloading {
			keepIDs[j.ID] = true
		}
	}

	var deletedIDs []string
	deleted := 0
	err = jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			if !keepIDs[string(k)] {
				toDelete = append(toDelete, k)
			}
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
			deletedIDs = append(deletedIDs, string(k))
			deleted++
		}
		return nil
	})

	if err == nil && deleted > 0 {
		fmt.Printf("[Jobs] Cleanup: deleted %d duplicate/old jobs\n", deleted)
	}
	if err == nil && jm.hub != nil {
		for _, id := range deletedIDs {
			jm.hub.publish(JobEvent{Type: "job_deleted", Job: &Job{ID: id}})
		}
	}
	return deleted, deletedIDs, err
}

// ClearCompletedJobs removes all done/skipped-manual jobs from the DB.
func (jm *JobManager) ClearCompletedJobs() ([]string, error) {
	var deletedIDs []string
	err := jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				return nil
			}
			if job.Status == StatusDone ||
				(job.Status == StatusSkipped && job.WatchlistID == "") {
				toDelete = append(toDelete, k)
				deletedIDs = append(deletedIDs, job.ID)
			}
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
	if jm.hub != nil {
		for _, id := range deletedIDs {
			jm.hub.publish(JobEvent{Type: "job_deleted", Job: &Job{ID: id}})
		}
	}
	return deletedIDs, err
}

// ClearAllJobs removes every job from the DB (key-by-key, no bucket drop).
func (jm *JobManager) ClearAllJobs() error {
	err := jm.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketJobs)
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		b.ForEach(func(k, v []byte) error {
			toDelete = append(toDelete, k)
			return nil
		})
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
	if jm.hub != nil {
		jm.hub.publish(JobEvent{Type: "queue_cleared", Job: nil})
	}
	return err
}
