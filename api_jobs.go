package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Jobs (queue, SSE, downloads) & History
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — jobs & history
// ─────────────────────────────────────────────────────────────────────────────

// audioContentType maps a downloaded file's extension to its real MIME type.
// Despite the app's name, downloads aren't FLAC-only — Amazon Music serves
// m4a and some fallback paths produce mp3 (see filemanager.go's own
// .flac/.mp3/.m4a handling) — so the browser-download response used to claim
// audio/flac unconditionally regardless of what was actually on disk.
func audioContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	default:
		return "audio/flac"
	}
}

func (s *Server) registerJobRoutes() {

	// ── Jobs ──────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/jobs", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req EnqueueBatchRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		req.UserID = userIDFromContext(r)
		// Same confinement as /downloads/track above — req.Settings.DownloadPath
		// otherwise reaches disk unconfined via buildOutputDir (S2).
		if req.Settings.DownloadPath != "" {
			cleaned, err := cleanLibraryPath(s.libraryRootFor(r), req.Settings.DownloadPath)
			if err != nil {
				writeV1Error(w, http.StatusBadRequest, "settings.downloadPath: "+err.Error())
				return
			}
			req.Settings.DownloadPath = cleaned
		}
		result, err := s.ctr.Jobs.EnqueueBatch(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, result)
	}))

	s.mux.Handle("GET /api/v1/jobs/stream", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		s.v1JobsStream(w, r)
	}))

	s.mux.Handle("GET /api/v1/jobs/{id}/download", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		// Serves the actual downloaded audio bytes, not just metadata about
		// the job — grouped with "manage" rather than "read" for that reason.
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		job, err := s.ctr.Jobs.GetJob(id)
		if err != nil {
			writeV1Error(w, http.StatusNotFound, "job not found")
			return
		}
		if user := GetUserFromContext(r); user != nil {
			if !user.IsAdmin && job.UserID != "" && job.UserID != user.UserID {
				writeV1Error(w, http.StatusForbidden, "forbidden")
				return
			}
		}
		if job.Status != StatusDone || job.FilePath == "" {
			writeV1Error(w, http.StatusBadRequest, "file not available")
			return
		}
		if _, statErr := os.Stat(job.FilePath); statErr != nil {
			writeV1Error(w, http.StatusNotFound, "file not found on server")
			return
		}
		f, err := os.Open(job.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, "failed to open file")
			return
		}
		defer f.Close()
		defer os.Remove(job.FilePath)

		filename := filepath.Base(job.FilePath)
		if info, statErr := f.Stat(); statErr == nil {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		w.Header().Set("Content-Type", audioContentType(filename))
		http.ServeContent(w, r, filename, time.Time{}, f)

		job.FilePath = ""
		job.UpdatedAt = time.Now()
		_ = s.ctr.Jobs.saveJob(&job)
		s.ctr.Jobs.notifyJob(&job)
	}))

	s.mux.Handle("DELETE /api/v1/jobs/completed", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		user := GetUserFromContext(r)
		s.ctr.History.ClearCompletedDownloads(userIDFromContext(r), user != nil && user.IsAdmin)
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/jobs", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		user := GetUserFromContext(r)
		s.ctr.History.ClearAllDownloads(userIDFromContext(r), user != nil && user.IsAdmin)
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Direct downloads ──────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/downloads/track", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req DownloadRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		s.ctr.Download.ApplySettingsFallbacks(&req, userIDFromContext(r))
		// output_dir otherwise reaches disk unconfined: any authenticated
		// session user (v1RequirePermission passes every non-API-key caller
		// through regardless of the "manage" perm — see its doc comment)
		// could point a download anywhere the server can write (S2).
		if req.OutputDir != "" {
			cleaned, err := cleanLibraryPath(s.libraryRootFor(r), req.OutputDir)
			if err != nil {
				writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
				return
			}
			req.OutputDir = cleaned
		}
		result, err := s.ctr.Download.DownloadTrack(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── History ───────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/history/downloads", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		userID := userIDFromContext(r)
		result, err := s.ctr.History.GetDownloadHistory(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("DELETE /api/v1/history/downloads", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		userID := userIDFromContext(r)
		if err := s.ctr.History.ClearDownloadHistory(userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/downloads/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		userID := userIDFromContext(r)
		if err := s.ctr.History.DeleteDownloadHistoryItem(id, userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.mux.Handle("GET /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		userID := userIDFromContext(r)
		result, err := s.ctr.History.GetFetchHistory(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var item backend.FetchHistoryItem
		if !decodeV1JSON(w, r, &item) {
			return
		}
		item.UserID = userIDFromContext(r)
		if err := s.ctr.History.AddFetchHistory(item); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		userID := userIDFromContext(r)
		if itemType := r.URL.Query().Get("type"); itemType != "" {
			if err := s.ctr.History.ClearFetchHistoryByType(itemType, userID); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := s.ctr.History.ClearFetchHistory(userID); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/fetch/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		userID := userIDFromContext(r)
		if err := s.ctr.History.DeleteFetchHistoryItem(id, userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.mux.Handle("GET /api/v1/history/downloads/export", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		user := GetUserFromContext(r)
		message, err := s.ctr.History.ExportFailedDownloads(userIDFromContext(r), user != nil && user.IsAdmin)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"message": message})
	}))
}
