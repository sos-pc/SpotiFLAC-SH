package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Admin-only maintenance operations
// ─────────────────────────────────────────────────────────────────────────────

import (
	"net/http"
	"os"

	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

// retagLegacyResult is the JSON payload returned by POST /api/v1/admin/retag-legacy.
type retagLegacyResult struct {
	Scanned   int      `json:"scanned"`
	Tagged    int      `json:"tagged"`
	Skipped   int      `json:"skipped"`
	Failed    int      `json:"failed"`
	FailedIDs []string `json:"failed_ids,omitempty"`
}

// registerAdminRoutes wires the /api/v1/admin/* endpoints. Each handler must
// guard with v1RequireAdmin since the dispatch layer only checks authentication.
func (s *Server) registerAdminRoutes() {
	s.mux.Handle("POST /api/v1/admin/retag-legacy", s.v1Auth(s.v1RetagLegacy))
}

// v1RetagLegacy walks every Done/Skipped job in BoltDB whose FilePath still
// exists on disk, and writes its SPOTIFY_ID into the file's tags if missing.
// Used once after deploying the tag-embedding change to retro-fit files
// downloaded before this commit. Idempotent: existing matching tags are skipped.
func (s *Server) v1RetagLegacy(w http.ResponseWriter, r *http.Request) {
	if !v1RequireAdmin(w, r) {
		return
	}

	jobs, err := s.ctr.Jobs.GetAllJobs()
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := retagLegacyResult{}
	for _, job := range jobs {
		if job.SpotifyID == "" || job.FilePath == "" {
			continue
		}
		if job.Status != StatusDone && job.Status != StatusSkipped {
			continue
		}
		if _, statErr := os.Stat(job.FilePath); statErr != nil {
			continue
		}
		result.Scanned++
		written, writeErr := meta.WriteSpotifyIDTag(job.FilePath, job.SpotifyID)
		switch {
		case writeErr != nil:
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, job.SpotifyID)
		case written:
			result.Tagged++
		default:
			result.Skipped++
		}
	}

	writeV1JSON(w, http.StatusOK, result)
}
