package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Watchlists
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"net/http"
)

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — watchlists
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerWatchlistRoutes() {
	s.mux.Handle("GET /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := userIDFromContext(r)
		result, err := s.ctr.Watcher.GetWatchlistsByUser(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req AddWatchlistRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		req.UserID = userIDFromContext(r)
		result, err := s.ctr.Watcher.AddWatchlist(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, result)
	}))

	s.mux.Handle("PUT /api/v1/watchlists/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		user := GetUserFromContext(r)
		if err := s.checkWatchlistOwnership(id, user); err != nil {
			writeV1Error(w, http.StatusForbidden, err.Error())
			return
		}
		var req UpdateWatchlistRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
		id := r.PathValue("id")
		user := GetUserFromContext(r)
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
		id := r.PathValue("id")
		user := GetUserFromContext(r)
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

	s.mux.Handle("GET /api/v1/watchlists/{id}/stats", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		user := GetUserFromContext(r)
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
		id := r.PathValue("id")
		user := GetUserFromContext(r)
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
