package jobs

import (
	"encoding/hex"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend"
)

const (
	// stagingReclaimEvery is how often the shared staging volume is swept for
	// abandoned job directories. Cheap: one readdir plus a shallow walk of each
	// candidate.
	stagingReclaimEvery = 6 * time.Hour

	// stagingStaleAfter is how long a job directory must have been untouched
	// before it is considered abandoned.
	//
	// The engine writes into /staging/<uuid4 hex>/ and the Go side moves the
	// file out and removes that directory when the download succeeds — a
	// deferred RemoveAll, which is exactly the kind of cleanup that does not
	// happen when the process is killed. Nothing else ever looked at the volume,
	// so anything left behind stayed forever: on the reference deployment a
	// 25 MB FLAC sat there for nine days, its track already ingested and in the
	// library, dropped between the atomic write and the deferred cleanup.
	//
	// Six hours, not six minutes, because the engine is a SEPARATE container and
	// can still be writing into a directory this process has given up on: the Go
	// client's own timeout is 10 minutes, after which it returns an error while
	// the engine keeps going. A sweep that trusted the clock alone would delete
	// a live download. Combined with the mtime rule below — the newest write
	// anywhere inside the directory, not the directory's own timestamp, which a
	// long download does not touch after creating its file — six hours is far
	// beyond any plausible single-track download and still reclaims the same
	// day.
	stagingStaleAfter = 6 * time.Hour
)

// reclaimStagingLoop deletes abandoned engine job directories, forever.
//
// Same shape as cleanupLoop and verifyLibraryLoop, and for the same reasons: a
// delay before the first pass so it does not compete with startup, then a fixed
// interval, with per-run panic recovery so one failure skips a pass instead of
// killing the goroutine for the life of the process.
func (jm *JobManager) reclaimStagingLoop() {
	select {
	case <-time.After(10 * time.Minute):
	case <-jm.ctx.Done():
		return
	}
	jm.runReclaimStagingSafely()
	ticker := time.NewTicker(stagingReclaimEvery)
	defer ticker.Stop()
	for {
		select {
		case <-jm.ctx.Done():
			return
		case <-ticker.C:
			jm.runReclaimStagingSafely()
		}
	}
}

func (jm *JobManager) runReclaimStagingSafely() {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[Staging] PANIC recovered in reclaim", "recover", r, "stack", string(debug.Stack()))
		}
	}()

	dir := backend.EngineStagingDir()
	if dir == "" || backend.EngineBaseURL() == "" {
		// No engine configured: the volume is not ours to touch.
		return
	}
	dirs, bytes, err := reclaimStaging(dir, stagingStaleAfter)
	if err != nil {
		slog.Warn("[Staging] Reclaim pass failed", "dir", dir, "err", err)
		return
	}
	if dirs > 0 {
		slog.Info("[Staging] Reclaimed abandoned job directories",
			"dirs", dirs, "bytes", bytes, "stale_after", stagingStaleAfter)
	}
}

// reclaimStaging removes job directories under root whose newest write is older
// than staleAfter. Returns how many directories went and how many bytes that
// freed.
//
// Only directories named like the engine's own — uuid4().hex, 32 lowercase hex
// characters — are candidates. Anything else on that volume was put there by
// something other than a download, and deleting by age alone would eventually
// find it.
func reclaimStaging(root string, staleAfter time.Duration) (int, int64, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil // nothing has been staged yet
		}
		return 0, 0, err
	}

	cutoff := time.Now().Add(-staleAfter)
	var reclaimed int
	var freed int64

	for _, e := range entries {
		if !e.IsDir() || !isJobDirName(e.Name()) {
			continue
		}
		path := filepath.Join(root, e.Name())
		newest, size, err := newestWrite(path)
		if err != nil {
			slog.Warn("[Staging] Could not inspect job directory", "path", path, "err", err)
			continue
		}
		if newest.After(cutoff) {
			continue // still being written, or only just finished
		}
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("[Staging] Could not remove abandoned job directory", "path", path, "err", err)
			continue
		}
		slog.Info("[Staging] Removed abandoned job directory",
			"path", path, "bytes", size, "idle_for", time.Since(newest).Truncate(time.Minute))
		reclaimed++
		freed += size
	}
	return reclaimed, freed, nil
}

// isJobDirName reports whether name is one the engine would have created:
// uuid.uuid4().hex, which is exactly 32 lowercase hex characters.
func isJobDirName(name string) bool {
	if len(name) != 32 {
		return false
	}
	if _, err := hex.DecodeString(name); err != nil {
		return false
	}
	// hex.DecodeString accepts uppercase; the engine never produces it, and
	// being strict keeps this from matching something else that happens to be
	// 32 hex characters in a different convention.
	for _, r := range name {
		if r >= 'A' && r <= 'F' {
			return false
		}
	}
	return true
}

// newestWrite returns the most recent modification time anywhere inside dir,
// and the total size of its contents.
//
// The directory's own mtime is not enough: it changes when an entry is created
// or removed, not while a file inside it grows. A download that takes an hour
// leaves the directory looking an hour old from the moment its file appeared,
// which is exactly how a sweep deletes a live job.
func newestWrite(dir string) (time.Time, int64, error) {
	newest := time.Time{}
	var size int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if t := info.ModTime(); t.After(newest) {
			newest = t
		}
		if !d.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return newest, size, err
}
