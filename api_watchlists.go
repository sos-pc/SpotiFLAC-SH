package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Watchlists
// ─────────────────────────────────────────────────────────────────────────────

import (
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — watchlists
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerWatchlistRoutes() {
	s.mux.Handle("GET /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		userID := userIDFromContext(r)
		result, err := s.ctr.Watcher.GetWatchlistsByUser(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req AddWatchlistRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		req.UserID = userIDFromContext(r)
		// AddWatchlist stores req.Settings.DownloadPath on the watchlist and
		// feeds it straight into every future sync's EnqueueBatch call
		// (watcher.go) — those internal calls never go through the HTTP
		// layer, so the same confinement applied to /downloads/track and
		// /jobs has to be enforced here too, at watchlist creation, or
		// every downstream sync would silently inherit an unconfined
		// download path (S2 — watchlist creation isn't admin-gated).
		if req.Settings.DownloadPath != "" {
			cleaned, err := cleanLibraryPath(s.libraryRootFor(r), req.Settings.DownloadPath)
			if err != nil {
				writeV1Error(w, http.StatusBadRequest, "settings.downloadPath: "+err.Error())
				return
			}
			req.Settings.DownloadPath = cleaned
		}
		result, err := s.ctr.Watcher.AddWatchlist(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, result)
	}))

	s.mux.Handle("PUT /api/v1/watchlists/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		var req UpdateWatchlistRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		req.ID = id
		if err := s.ctr.Watcher.UpdateWatchlist(req); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/watchlists/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		if err := s.ctr.Watcher.RemoveWatchlist(id); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.mux.Handle("POST /api/v1/watchlists/{id}/sync", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		if err := s.ctr.Watcher.SyncWatchlist(id); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusAccepted, map[string]bool{"ok": true})
	}))

	s.mux.Handle("POST /api/v1/watchlists/{id}/repair", s.v1Auth(s.v1RepairWatchlist))

	// Read-only: fetches live Spotify data (bounded 30s timeout) but never
	// enqueues downloads, deletes files, or mutates the watchlist — safe to
	// call synchronously, unlike sync/repair which run in the background.
	s.mux.Handle("GET /api/v1/watchlists/{id}/freshness", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		result, err := s.ctr.Watcher.CheckWatchlistFreshness(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/watchlists/{id}/stats", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		result, err := s.ctr.Watcher.GetWatchlistStats(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/watchlists/{id}/history", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "read") {
			return
		}
		id := r.PathValue("id")
		user := auth.GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		result, err := s.ctr.Watcher.GetWatchlistHistory(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))
}
