package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Watchlists
// ─────────────────────────────────────────────────────────────────────────────

import (
	"errors"
	"fmt"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/watcher"
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

	// Bulk add, for the playlist picker. Ticking twelve boxes and getting
	// twelve round-trips — each able to fail on its own, with no way to say
	// which — is the experience this avoids.
	//
	// Always 200 with per-item outcomes, never a single error: a bulk add is
	// partially successful by nature, and eleven watchlists that worked must
	// not be reported as a failure because the twelfth had a dead URL.
	s.mux.Handle("POST /api/v1/watchlists/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var body struct {
			SpotifyURLs   []string `json:"spotify_urls"`
			IntervalHours int      `json:"interval_hours"`
			SyncDeletions bool     `json:"sync_deletions"`
		}
		if !decodeV1JSON(w, r, &body) {
			return
		}
		if len(body.SpotifyURLs) == 0 {
			writeV1Error(w, http.StatusBadRequest, "spotify_urls must not be empty")
			return
		}
		// A ceiling, because this walks every playlist's metadata before it
		// returns and the request would otherwise be unbounded. The picker
		// shows one profile at a time; the largest seen has 57.
		const maxBatch = 200
		if len(body.SpotifyURLs) > maxBatch {
			writeV1Error(w, http.StatusBadRequest,
				fmt.Sprintf("too many playlists in one request (%d, max %d)", len(body.SpotifyURLs), maxBatch))
			return
		}
		userID := userIDFromContext(r)
		reqs := make([]watcher.AddWatchlistRequest, 0, len(body.SpotifyURLs))
		for _, u := range body.SpotifyURLs {
			reqs = append(reqs, watcher.AddWatchlistRequest{
				SpotifyURL:    u,
				IntervalHours: body.IntervalHours,
				SyncDeletions: body.SyncDeletions,
				UserID:        userID,
			})
		}
		writeV1JSON(w, http.StatusOK, s.ctr.Watcher.AddWatchlists(reqs))
	}))

	s.mux.Handle("POST /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequirePermission(w, r, "manage") {
			return
		}
		var req watcher.AddWatchlistRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		req.UserID = userIDFromContext(r)
		// Kept as defence in depth, but read its history before trusting the
		// old justification: this used to say the stored path "feeds every
		// future sync's EnqueueBatch call". It does not — watchlistJobSettings
		// resolves from the user's settings and ignores WatchedPlaylist.Settings,
		// whose own doc comment calls that copy legacy. And downloadPath became
		// instance-scoped, so libraryRootFor now returns the operator's root
		// rather than one the caller chose for themselves.
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
		var req watcher.UpdateWatchlistRequest
		if !decodeV1JSON(w, r, &req) {
			return
		}
		req.ID = id
		if err := s.ctr.Watcher.UpdateWatchlist(req); err != nil {
			// A name already in use is the caller's input being wrong, not the
			// server failing — and the UI shows this message verbatim, so a 500
			// would read as a bug rather than as something to fix by typing
			// something else.
			if errors.Is(err, watcher.ErrNameTaken) {
				writeV1Error(w, http.StatusConflict, err.Error())
				return
			}
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
