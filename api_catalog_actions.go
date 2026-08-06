package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Admin-only closed write actions on the SQLite catalog
// ─────────────────────────────────────────────────────────────────────────────
//
// Reads of the catalog are open (see api_catalog.go): arbitrary filtering is
// safe once no client string reaches the SQL. Writes are not, and deliberately
// so — each one here is a named operation with a stated invariant, never an
// arbitrary UPDATE.
//
// The reason is what the catalog *is*. jobs_catalog.go states it plainly: the
// BoltDB job is the user-facing source of truth and the catalog is a long-term
// audit trail mirroring it and the files on disk. Editing the mirror does not
// change the thing mirrored — a genre "fixed" here leaves the FLAC tag alone
// and gets overwritten by the next rebuild. An arbitrary write on a mirror is
// the worst of both worlds: strong enough to corrupt, too weak to change
// anything real.
//
// Foreign keys are on, which protects referential integrity, but nothing
// enforces the *semantic* invariants — a status that no code produces, a
// quality_rank inconsistent with quality. Readers assume those hold because
// the write functions maintain them. A closed action can be tested against its
// invariant; a generic write console cannot, because it can produce any state
// at all and no one knows which.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
)

// checkDeletedRequest is the body of POST /admin/library-check-deleted.
type checkDeletedRequest struct {
	// Apply must be set explicitly. The default is a dry run: a write action
	// should be inspectable before it runs, and the safe default is the one
	// that changes nothing.
	Apply bool `json:"apply"`
}

// checkDeletedResult reports what the pass saw and what it changed.
type checkDeletedResult struct {
	Applied bool `json:"applied"` // false = nothing was written
	Checked int  `json:"checked"`

	// WentMissing counts rows the catalog called present but whose file is not
	// on disk. This is the number that matters: until now nothing ever set
	// "missing", so every row claimed present without that claim ever being
	// checked.
	WentMissing int `json:"went_missing"`
	// CameBack counts rows marked missing whose file is on disk again — a
	// restored backup, or a path repaired outside the app.
	CameBack  int `json:"came_back"`
	Unchanged int `json:"unchanged"`
	// Failed counts rows whose path could not be checked for a reason other
	// than absence (a permission error, say). These are left untouched: an
	// unreadable directory must not be recorded as a missing library.
	Failed int `json:"failed,omitempty"`

	// MissingSample lists a few newly-missing paths so the result can be
	// sanity-checked without querying the table afterwards.
	MissingSample []string `json:"missing_sample,omitempty"`
	TimedOut      bool     `json:"timed_out,omitempty"`
}

const (
	// checkDeletedSampleLimit caps the sample; a library that lost thousands of
	// files should not return thousands of strings.
	checkDeletedSampleLimit = 50
	// checkDeletedTimeout bounds the pass. 2589 stat() calls are fast, but a
	// stalled network mount must not wedge the request forever.
	checkDeletedTimeout = 2 * time.Minute
)

func (s *Server) registerCatalogActionRoutes() {
	s.mux.Handle("POST /api/v1/admin/library-check-deleted", s.v1Auth(s.v1CheckDeletedFiles))
	s.mux.Handle("POST /api/v1/admin/library-redownload-missing", s.v1Auth(s.v1RedownloadMissing))
}

// v1ReconcileLibrary checks every non-deleted library_files row against disk
// and reconciles its status.
//
// Invariant it maintains, and the only one: library_files.status reflects
// whether file_path exists on disk right now. Nothing else is touched — not
// quality, not provider, not the path itself. Repairing a *moved* file is
// library-rebuild's job, which matches by SPOTIFY_ID tag; this action only
// answers "is it there?".
//
// Why it exists: three comments in the codebase refer to a "rescan task" that
// marks files missing. That task does not exist. StatusMissing was written
// only by tests, so all 2589 rows in prod claimed "present" without anything
// ever having verified it.
func (s *Server) v1CheckDeletedFiles(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	if s.ctr == nil || s.ctr.Catalog == nil {
		writeV1Error(w, http.StatusInternalServerError, "catalog database is not available")
		return
	}

	var req checkDeletedRequest
	if r.Body != nil {
		// An absent or empty body is a dry run, which is the safe default.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	ctx, cancel := context.WithTimeout(r.Context(), checkDeletedTimeout)
	defer cancel()

	files, err := db.ListCheckableLibraryFiles(ctx, s.ctr.Catalog)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := checkDeletedResult{Applied: req.Apply}
	for _, f := range files {
		if ctx.Err() != nil {
			result.TimedOut = true
			break
		}
		result.Checked++

		onDisk, err := statLibraryFile(f.FilePath)
		if err != nil {
			// Could not tell. Leaving the row alone is the conservative
			// choice: an unreadable mount must not be recorded as a library
			// that lost every file.
			result.Failed++
			continue
		}

		switch {
		case !onDisk && f.Status != db.StatusMissing:
			result.WentMissing++
			if len(result.MissingSample) < checkDeletedSampleLimit {
				result.MissingSample = append(result.MissingSample, f.FilePath)
			}
			if req.Apply {
				if err := db.UpdateLibraryFileStatus(ctx, s.ctr.Catalog, f.ID, db.StatusMissing); err != nil {
					slog.Warn("[Library] check-deleted: could not mark missing", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		case onDisk && f.Status == db.StatusMissing:
			result.CameBack++
			if req.Apply {
				if err := db.UpdateLibraryFileStatus(ctx, s.ctr.Catalog, f.ID, db.StatusPresent); err != nil {
					slog.Warn("[Library] check-deleted: could not mark present", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		default:
			result.Unchanged++
		}
	}

	slog.Info("[Library] Check deleted files",
		"applied", result.Applied, "checked", result.Checked,
		"went_missing", result.WentMissing, "came_back", result.CameBack,
		"failed", result.Failed, "timed_out", result.TimedOut)

	writeV1JSON(w, http.StatusOK, result)
}

// statLibraryFile reports whether path is an existing file, keeping "not
// there" and "could not tell" apart — the two must lead to different
// decisions: the first is a finding, the second is a reason to do nothing.
//
// Deliberately not util.FileExists, which returns a bare bool and so folds a
// permission error into "absent". Here that would record an unreadable mount
// as a library that lost every one of its files.
func statLibraryFile(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("empty path")
	}
	info, err := os.Stat(path)
	if err == nil {
		return !info.IsDir(), nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// ─────────────────────────────────────────────────────────────────────────────

// redownloadMissingRequest is the body of POST /admin/library-redownload-missing.
type redownloadMissingRequest struct {
	// Apply defaults to false, same as check-deleted: a write action should be
	// inspectable before it runs.
	Apply bool `json:"apply"`
}

// redownloadMissingResult reports what was found and what was queued.
type redownloadMissingResult struct {
	Applied bool `json:"applied"`
	// Missing is how many library_files rows are marked missing right now.
	Missing int `json:"missing"`
	// Queued is how many download jobs were actually created. It can be lower
	// than Missing: EnqueueBatch runs its own dedup and drops a track that
	// already has an active job.
	Queued  int `json:"queued"`
	Skipped int `json:"skipped"`
	// NoMetadata counts missing rows whose track row could not be read, so no
	// download could be described. Reported rather than silently dropped.
	NoMetadata int      `json:"no_metadata,omitempty"`
	Tracks     []string `json:"tracks,omitempty"`
}

// redownloadSampleLimit caps the track list in the response.
const redownloadSampleLimit = 50

// v1RedownloadMissing re-queues a download for every track whose file the
// catalog knows is gone.
//
// Why this has to exist. A watchlist sync only looks at tracks that are *new*
// to the playlist — watcher.go drops anything in knownIDs before the enqueue
// path is reached, and the enqueue path is the only place that stats the disk
// (checkCatalogDedup). So a file deleted outside SpotiFLAC is never noticed by
// a sync, however many times it runs. Repair does not cover it either: it is
// retag + library-rebuild + M3U8, and a disk walk cannot see a file that is
// not there. Before this, the only way back was to remove the track from the
// watchlist and re-add it so it counted as new.
//
// It pairs with library-check-deleted, which is what marks a row missing in
// the first place: that one detects, this one repairs.
func (s *Server) v1RedownloadMissing(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	if s.ctr == nil || s.ctr.Catalog == nil || s.ctr.Jobs == nil {
		writeV1Error(w, http.StatusInternalServerError, "catalog or job manager is not available")
		return
	}

	var req redownloadMissingRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	ctx, cancel := context.WithTimeout(r.Context(), checkDeletedTimeout)
	defer cancel()

	ids, err := db.ListMissingSpotifyIDs(ctx, s.ctr.Catalog)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := redownloadMissingResult{Applied: req.Apply, Missing: len(ids)}

	var tracks []jobs.JobTrack
	for _, id := range ids {
		t, err := db.GetTrack(ctx, s.ctr.Catalog, id)
		if err != nil || t == nil {
			// A library_file with no track row should not happen (the foreign
			// key is ON DELETE RESTRICT), but a download cannot be described
			// without a title and artist, so count it rather than guess.
			slog.Warn("[Library] redownload-missing: no track metadata", "spotify_id", id, "err", err)
			result.NoMetadata++
			continue
		}
		if len(result.Tracks) < redownloadSampleLimit {
			result.Tracks = append(result.Tracks, t.ArtistName+" — "+t.Name)
		}
		tracks = append(tracks, jobs.JobTrack{
			SpotifyID:   t.SpotifyID,
			TrackName:   t.Name,
			ArtistName:  t.ArtistName,
			AlbumName:   t.AlbumName,
			AlbumArtist: t.AlbumArtist,
			ReleaseDate: t.ReleaseDate,
			CoverURL:    t.CoverURL,
			TrackNumber: t.TrackNumber,
			DiscNumber:  t.DiscNumber,
			Copyright:   t.Copyright,
			DurationMs:  t.DurationMs,
		})
	}

	if !req.Apply || len(tracks) == 0 {
		writeV1JSON(w, http.StatusOK, result)
		return
	}

	// Enqueued as a manual batch: no WatchlistID. Attributing these to a
	// watchlist would make the watcher treat them as part of a sync and
	// regenerate that playlist's M3U8 from a partial batch.
	userID := userIDFromContext(r)
	resp, err := s.ctr.Jobs.EnqueueBatch(jobs.EnqueueBatchRequest{
		Tracks:   tracks,
		Settings: serverJobSettings(EffectiveDownloadSettings(s.ctr.Auth, userID), ""),
		UserID:   userID,
	})
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	result.Queued = resp.Enqueued
	result.Skipped = resp.Skipped

	slog.Info("[Library] Redownload missing",
		"missing", result.Missing, "queued", result.Queued,
		"skipped", result.Skipped, "no_metadata", result.NoMetadata)

	writeV1JSON(w, http.StatusOK, result)
}
