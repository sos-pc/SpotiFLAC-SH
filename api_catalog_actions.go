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

	"github.com/afkarxyz/SpotiFLAC/backend/db"
)

// reconcileRequest is the body of POST /admin/db/library/reconcile.
type reconcileRequest struct {
	// Apply must be set explicitly. The default is a dry run: a write action
	// should be inspectable before it runs, and the safe default is the one
	// that changes nothing.
	Apply bool `json:"apply"`
}

// reconcileResult reports what the pass saw and what it changed.
type reconcileResult struct {
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
	// reconcileSampleLimit caps the sample; a library that lost thousands of
	// files should not return thousands of strings.
	reconcileSampleLimit = 50
	// reconcileTimeout bounds the pass. 2589 stat() calls are fast, but a
	// stalled network mount must not wedge the request forever.
	reconcileTimeout = 2 * time.Minute
)

func (s *Server) registerCatalogActionRoutes() {
	s.mux.Handle("POST /api/v1/admin/db/library/reconcile", s.v1Auth(s.v1ReconcileLibrary))
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
func (s *Server) v1ReconcileLibrary(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}
	if s.ctr == nil || s.ctr.Catalog == nil {
		writeV1Error(w, http.StatusInternalServerError, "catalog database is not available")
		return
	}

	var req reconcileRequest
	if r.Body != nil {
		// An absent or empty body is a dry run, which is the safe default.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	ctx, cancel := context.WithTimeout(r.Context(), reconcileTimeout)
	defer cancel()

	files, err := db.ListReconcilableLibraryFiles(ctx, s.ctr.Catalog)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := reconcileResult{Applied: req.Apply}
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
			if len(result.MissingSample) < reconcileSampleLimit {
				result.MissingSample = append(result.MissingSample, f.FilePath)
			}
			if req.Apply {
				if err := db.UpdateLibraryFileStatus(ctx, s.ctr.Catalog, f.ID, db.StatusMissing); err != nil {
					slog.Warn("[Catalog] reconcile: could not mark missing", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		case onDisk && f.Status == db.StatusMissing:
			result.CameBack++
			if req.Apply {
				if err := db.UpdateLibraryFileStatus(ctx, s.ctr.Catalog, f.ID, db.StatusPresent); err != nil {
					slog.Warn("[Catalog] reconcile: could not mark present", "id", f.ID, "err", err)
					result.Failed++
				}
			}
		default:
			result.Unchanged++
		}
	}

	slog.Info("[Catalog] Library reconcile",
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
