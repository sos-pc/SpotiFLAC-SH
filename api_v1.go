package main

// ─────────────────────────────────────────────────────────────────────────────
// REST API v1 — /api/v1/*
//
// Auth : JWT Bearer (frontend) ou X-API-Key (applications externes).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func writeV1JSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeV1Error(w http.ResponseWriter, status int, msg string) {
	writeV1JSON(w, status, map[string]string{"error": msg})
}

// cleanAbsPath normalizes a filesystem path and rejects anything that is not
// absolute after cleaning (prevents ../traversal tricks and relative paths).
func cleanAbsPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("path must be absolute")
	}
	return clean, nil
}

// cleanAbsPaths validates a slice of paths, returning the cleaned slice or the
// first error encountered.
func cleanAbsPaths(paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		c, err := cleanAbsPath(p)
		if err != nil {
			return nil, fmt.Errorf("path[%d]: %w", i, err)
		}
		out[i] = c
	}
	return out, nil
}

// v1CORS gère les headers CORS pour l'API v1 (inclut X-API-Key).
func v1CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Middleware auth v1 — JWT Bearer ou X-API-Key
// ─────────────────────────────────────────────────────────────────────────────

// v1Auth enveloppe un handler avec CORS + local bypass + auth JWT/API Key.
func (s *Server) v1Auth(next http.HandlerFunc) http.Handler {
	return v1CORSMiddleware(localBypassMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. JWT Bearer
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != "" {
			if claims, err := ValidateJWT(token); err == nil {
				ctx := context.WithValue(r.Context(), contextKeyUser, claims)
				next(w, r.WithContext(ctx))
				return
			}
		}

		// 2. X-API-Key
		if apiKey := r.Header.Get("X-API-Key"); apiKey != "" && s.ctr.Auth != nil {
			if claims, ok := s.ctr.Auth.ValidateAPIKey(apiKey); ok {
				ctx := context.WithValue(r.Context(), contextKeyUser, claims)
				next(w, r.WithContext(ctx))
				return
			}
		}

		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
	})))
}

// v1RequireAdmin renvoie 403 si l'utilisateur n'est pas admin.
func v1RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := GetUserFromContext(r)
	if user == nil || !user.IsAdmin {
		writeV1Error(w, http.StatusForbidden, "admin required")
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Route registration
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerV1Routes() {
	a := s.app

	// ── Auth & API Keys ───────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/auth/login", v1CORSMiddleware(http.HandlerFunc(s.v1Login)))

	s.mux.Handle("POST /api/v1/auth/local", v1CORSMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localBypassEnabled() || !isLocalIP(r) {
			writeV1Error(w, http.StatusForbidden, "local bypass not enabled")
			return
		}
		profile := &UserProfile{ID: "local-admin", DisplayName: "Local Admin", IsAdmin: true}
		token, err := GenerateJWT(profile)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, "failed to generate token")
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]interface{}{
			"token": token,
			"user":  map[string]interface{}{"id": profile.ID, "display_name": profile.DisplayName, "is_admin": profile.IsAdmin},
		})
	})))
	s.mux.Handle("GET /api/v1/auth/me", s.v1Auth(s.v1Me))
	s.mux.Handle("GET /api/v1/auth/keys", s.v1Auth(s.v1ListAPIKeys))
	s.mux.Handle("POST /api/v1/auth/keys", s.v1Auth(s.v1CreateAPIKey))
	s.mux.Handle("DELETE /api/v1/auth/keys/{id}", s.v1Auth(s.v1RevokeAPIKey))
	s.mux.Handle("GET /api/v1/auth/tidal/url", s.v1Auth(s.v1TidalAuthURL))
	s.mux.Handle("POST /api/v1/auth/tidal/callback", s.v1Auth(s.v1TidalCallback))
	s.mux.Handle("GET /api/v1/auth/tidal/status", s.v1Auth(s.v1TidalStatus))
	s.mux.Handle("DELETE /api/v1/auth/tidal", s.v1Auth(s.v1TidalDisconnect))
	s.mux.Handle("POST /api/v1/auth/tidal/device/start", s.v1Auth(s.v1TidalDeviceStart))
	s.mux.Handle("POST /api/v1/auth/tidal/device/poll", s.v1Auth(s.v1TidalDevicePoll))
	s.mux.Handle("GET /api/v1/apis/status", s.v1Auth(s.v1APIStatus))
	s.mux.Handle("GET /api/v1/apis/proxies", s.v1Auth(s.v1GetProxies))
	s.mux.Handle("PUT /api/v1/apis/proxies", s.v1Auth(s.v1PutProxies))

	// ── Search ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/search", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		url := r.URL.Query().Get("url")
		if url == "" {
			writeV1Error(w, http.StatusBadRequest, "url query param required")
			return
		}
		batchStr := r.URL.Query().Get("batch")
		batch := batchStr == "true" || batchStr == "1"
		result, err := a.GetSpotifyMetadata(SpotifyMetadataRequest{URL: url, Batch: batch})
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("GET /api/v1/search/query", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		searchType := r.URL.Query().Get("type")
		if q == "" {
			writeV1Error(w, http.StatusBadRequest, "q query param required")
			return
		}
		limit := 10
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 {
				limit = v
			}
		}
		offset := 0
		if o := r.URL.Query().Get("offset"); o != "" {
			if v, err := strconv.Atoi(o); err == nil && v >= 0 {
				offset = v
			}
		}
		if searchType != "" {
			result, err := a.SearchSpotifyByType(SpotifySearchByTypeRequest{
				Query:      q,
				SearchType: searchType,
				Limit:      limit,
				Offset:     offset,
			})
			if err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, result)
		} else {
			result, err := a.SearchSpotify(SpotifySearchRequest{Query: q, Limit: limit})
			if err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, result)
		}
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/preview", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		url, err := a.GetPreviewURL(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/availability", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		result, err := a.CheckTrackAvailability(id)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	s.mux.Handle("GET /api/v1/tracks/{id}/links", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		region := r.URL.Query().Get("region")
		result, err := a.GetStreamingURLs(id, region)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]json.RawMessage{"urls": json.RawMessage(result)})
	}))

	// ── Jobs ──────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/jobs", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r)
		queue := a.GetDownloadQueue()
		if user != nil {
			jobs, err := s.ctr.Jobs.GetAllJobs()
			if err == nil {
				allowedIDs := make(map[string]bool)
				for _, j := range jobs {
					if j.UserID == "" || j.UserID == user.UserID {
						allowedIDs[j.ID] = true
					}
				}
				filtered := queue.Queue[:0]
				for _, item := range queue.Queue {
					if allowedIDs[item.ID] {
						filtered = append(filtered, item)
					}
				}
				queue.Queue = filtered
			}
		}
		writeV1JSON(w, http.StatusOK, queue)
	}))

	s.mux.Handle("POST /api/v1/jobs", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req EnqueueBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if user := GetUserFromContext(r); user != nil {
			req.UserID = user.UserID
		}
		result, err := s.ctr.Jobs.EnqueueBatch(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, result)
	}))

	// SSE — streaming temps réel de la progression
	s.mux.Handle("GET /api/v1/jobs/stream", s.v1Auth(s.v1JobsStream))

	s.mux.Handle("DELETE /api/v1/jobs/completed", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		a.ClearCompletedDownloads()
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/jobs", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		a.ClearAllDownloads()
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Watchlists ────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/watchlists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
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
		if user := GetUserFromContext(r); user != nil {
			req.UserID = user.UserID
		}
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

	// ── History ───────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/history/downloads", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		result, err := a.GetDownloadHistory(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("DELETE /api/v1/history/downloads", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		if err := a.ClearDownloadHistory(userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/downloads/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		if err := a.DeleteDownloadHistoryItem(id, userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	s.mux.Handle("GET /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		result, err := a.GetFetchHistory(userID)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var item backend.FetchHistoryItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if user := GetUserFromContext(r); user != nil {
			item.UserID = user.UserID
		}
		if err := a.AddFetchHistory(item); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusCreated, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/fetch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		if itemType := r.URL.Query().Get("type"); itemType != "" {
			if err := a.ClearFetchHistoryByType(itemType, userID); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		} else {
			if err := a.ClearFetchHistory(userID); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("DELETE /api/v1/history/fetch/{id}", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		userID := ""
		if user := GetUserFromContext(r); user != nil {
			userID = user.UserID
		}
		if err := a.DeleteFetchHistoryItem(id, userID); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	// ── Settings ──────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/settings", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		user := GetUserFromContext(r)
		if user != nil && s.ctr.Auth != nil {
			if profile, err := s.ctr.Auth.GetUser(user.UserID); err == nil && len(profile.Settings) > 0 {
				writeV1JSON(w, http.StatusOK, profile.Settings)
				return
			}
		}
		settings, err := a.LoadSettings()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, settings)
	}))

	s.mux.Handle("PUT /api/v1/settings", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var settings map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		user := GetUserFromContext(r)
		if user != nil && s.ctr.Auth != nil {
			if err := s.ctr.Auth.SaveUserSettings(user.UserID, settings); err != nil {
				writeV1Error(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
		if err := a.SaveSettings(settings); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Files ─────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/files", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ListDirectoryFiles(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/files/audio", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ListAudioFilesInDir(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("GET /api/v1/files/metadata", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		result, err := a.ReadFileMetadata(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/files/rename", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			OldPath string `json:"old_path"`
			NewName string `json:"new_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if _, err := cleanAbsPath(params.OldPath); err != nil {
			writeV1Error(w, http.StatusBadRequest, "old_path: "+err.Error())
			return
		}
		if err := a.RenameFileTo(params.OldPath, params.NewName); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	// ── Audio ─────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/audio/analyze", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.AnalyzeTrack(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/audio/convert", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req ConvertAudioRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.ConvertAudio(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── Media ─────────────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/media/lyrics", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req LyricsDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadLyrics(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/cover", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req CoverDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadCover(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── System ────────────────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/system/info", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		osInfo, _ := a.GetOSInfo()
		configPath, _ := a.GetConfigPath()
		homeDir, _ := os.UserHomeDir()
		writeV1JSON(w, http.StatusOK, map[string]string{
			"os":          osInfo,
			"config_path": configPath,
			"home_dir":    homeDir,
			"version":     "v1",
		})
	}))

	s.mux.Handle("GET /api/v1/system/ffmpeg", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		installed, err := a.CheckFFmpegInstalled()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		ffprobeInstalled, _ := a.IsFFprobeInstalled()
		ffmpegPath, _ := a.GetFFmpegPath()
		writeV1JSON(w, http.StatusOK, map[string]interface{}{
			"installed":         installed,
			"ffprobe_installed": ffprobeInstalled,
			"ffmpeg_path":       ffmpegPath,
		})
	}))

	s.mux.Handle("GET /api/v1/system/defaults", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		writeV1JSON(w, http.StatusOK, a.GetDefaults())
	}))

	// ── Downloads (direct) ────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/downloads/track", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req DownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadTrack(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/header", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req HeaderDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadHeader(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/gallery", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req GalleryImageDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadGalleryImage(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	s.mux.Handle("POST /api/v1/media/avatar", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var req AvatarDownloadRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.DownloadAvatar(req)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, result)
	}))

	// ── Jobs (legacy queue ops) ───────────────────────────────────────────
	s.mux.Handle("GET /api/v1/jobs/progress", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		writeV1JSON(w, http.StatusOK, a.GetDownloadProgress())
	}))

	s.mux.Handle("DELETE /api/v1/jobs/pending", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		a.CancelAllQueuedItems()
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("POST /api/v1/jobs/legacy/enqueue", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			SpotifyID  string `json:"spotify_id"`
			TrackName  string `json:"track_name"`
			ArtistName string `json:"artist_name"`
			AlbumName  string `json:"album_name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		id := a.AddToDownloadQueue(params.SpotifyID, params.TrackName, params.ArtistName, params.AlbumName)
		writeV1JSON(w, http.StatusOK, map[string]string{"id": id})
	}))

	s.mux.Handle("POST /api/v1/jobs/legacy/skip", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			ItemID   string `json:"item_id"`
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		a.SkipDownloadItem(params.ItemID, params.FilePath)
		w.WriteHeader(http.StatusNoContent)
	}))

	s.mux.Handle("POST /api/v1/jobs/legacy/fail", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			ItemID   string `json:"item_id"`
			ErrorMsg string `json:"error_msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		a.MarkDownloadItemFailed(params.ItemID, params.ErrorMsg)
		w.WriteHeader(http.StatusNoContent)
	}))

	// ── Files (extended) ──────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/files/sizes", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.GetFileSizes(cleaned))
	}))

	// Admin-only: returns raw file contents.
	s.mux.Handle("GET /api/v1/files/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		path, err := cleanAbsPath(r.URL.Query().Get("path"))
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := a.ReadImageAsBase64(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"data": data})
	}))

	// Admin-only: returns raw file contents.
	s.mux.Handle("POST /api/v1/files/read", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		if !v1RequireAdmin(w, r) {
			return
		}
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		path, err := cleanAbsPath(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		content, err := a.ReadTextFile(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"content": content})
	}))

	s.mux.Handle("POST /api/v1/files/rename/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.RenameFilesByMetadata(cleaned, params.Format))
	}))

	s.mux.Handle("POST /api/v1/files/rename/preview", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Files  []string `json:"files"`
			Format string   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		cleaned, err := cleanAbsPaths(params.Files)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.PreviewRenameFiles(cleaned, params.Format))
	}))

	s.mux.Handle("POST /api/v1/files/upload/image", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			Filename   string `json:"filename"`
			Base64Data string `json:"base64_data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		url, err := a.UploadImageBytes(params.Filename, params.Base64Data)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("POST /api/v1/files/upload/path", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePath string `json:"file_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		path, err := cleanAbsPath(params.FilePath)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		url, err := a.UploadImage(path)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
	}))

	s.mux.Handle("POST /api/v1/files/m3u8", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			M3U8Name          string   `json:"m3u8_name"`
			OutputDir         string   `json:"output_dir"`
			FilePaths         []string `json:"file_paths"`
			JellyfinMusicPath string   `json:"jellyfin_music_path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanAbsPath(params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		cleanedPaths, err := cleanAbsPaths(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := a.CreateM3U8File(params.M3U8Name, outputDir, cleanedPaths, params.JellyfinMusicPath); err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))

	s.mux.Handle("POST /api/v1/files/exists", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			OutputDir string                      `json:"output_dir"`
			RootDir   string                      `json:"root_dir"`
			Tracks    []CheckFileExistenceRequest `json:"tracks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		outputDir, err := cleanAbsPath(params.OutputDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "output_dir: "+err.Error())
			return
		}
		rootDir, err := cleanAbsPath(params.RootDir)
		if err != nil {
			writeV1Error(w, http.StatusBadRequest, "root_dir: "+err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, a.CheckFilesExistence(outputDir, rootDir, params.Tracks))
	}))

	// ── Audio (batch) ─────────────────────────────────────────────────────
	s.mux.Handle("POST /api/v1/audio/analyze/batch", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		var params struct {
			FilePaths []string `json:"file_paths"`
		}
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		result, err := a.AnalyzeMultipleTracks(params.FilePaths)
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, json.RawMessage(result))
	}))

	// ── History (export) ──────────────────────────────────────────────────
	s.mux.Handle("GET /api/v1/history/downloads/export", s.v1Auth(func(w http.ResponseWriter, r *http.Request) {
		message, err := a.ExportFailedDownloads()
		if err != nil {
			writeV1Error(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeV1JSON(w, http.StatusOK, map[string]string{"message": message})
	}))
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers auth
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1Login(w http.ResponseWriter, r *http.Request) {
	if !s.loginRL.Allow(remoteIP(r)) {
		w.Header().Set("Retry-After", "300")
		writeV1Error(w, http.StatusTooManyRequests, "too many login attempts, please wait 5 minutes")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if s.ctr.Auth == nil {
		writeV1Error(w, http.StatusInternalServerError, "auth not initialized")
		return
	}
	profile, err := s.ctr.Auth.AuthenticateWithJellyfin(req.Username, req.Password)
	if err != nil {
		writeV1Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	token, err := GenerateJWT(profile)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"user": map[string]interface{}{
			"id":           profile.ID,
			"display_name": profile.DisplayName,
			"is_admin":     profile.IsAdmin,
		},
	})
}

func (s *Server) v1Me(w http.ResponseWriter, r *http.Request) {
	claims := GetUserFromContext(r)
	if claims == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]interface{}{
		"id":           claims.UserID,
		"display_name": claims.DisplayName,
		"is_admin":     claims.IsAdmin,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Handlers API Keys
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	user := GetUserFromContext(r)
	if user == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	keys, err := s.ctr.Auth.ListAPIKeys(user.UserID)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []APIKey{}
	}
	writeV1JSON(w, http.StatusOK, keys)
}

func (s *Server) v1CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	user := GetUserFromContext(r)
	if user == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeV1Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"read", "download"}
	}
	rawKey, key, err := s.ctr.Auth.CreateAPIKey(user.UserID, req.Name, req.Permissions)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("failed to create key: %v", err))
		return
	}
	// Retourner la clé brute une seule fois + les métadonnées
	writeV1JSON(w, http.StatusCreated, map[string]interface{}{
		"key":  rawKey,
		"id":   key.ID,
		"name": key.Name,
		"permissions": key.Permissions,
		"created_at": key.CreatedAt,
	})
}

func (s *Server) v1RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	user := GetUserFromContext(r)
	if user == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	keyID := r.PathValue("id")
	if err := s.ctr.Auth.RevokeAPIKey(keyID, user.UserID); err != nil {
		writeV1Error(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─────────────────────────────────────────────────────────────────────────────
// Tidal Auth
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1TidalAuthURL(w http.ResponseWriter, r *http.Request) {
	url := backend.GenerateTidalAuthURL()
	writeV1JSON(w, http.StatusOK, map[string]string{"url": url})
}

func (s *Server) v1TidalCallback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallbackURL string `json:"callback_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.CallbackURL == "" {
		writeV1Error(w, http.StatusBadRequest, "callback_url is required")
		return
	}
	if err := backend.ExchangeTidalAuthCode(req.CallbackURL); err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) v1TidalStatus(w http.ResponseWriter, r *http.Request) {
	token := backend.LoadTidalToken()
	if token == nil {
		writeV1JSON(w, http.StatusOK, map[string]interface{}{"connected": false})
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]interface{}{
		"connected":  true,
		"expires_at": token.ExpiresAt,
	})
}

func (s *Server) v1TidalDisconnect(w http.ResponseWriter, r *http.Request) {
	backend.DeleteTidalToken()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) v1TidalDeviceStart(w http.ResponseWriter, r *http.Request) {
	resp, err := backend.StartTidalDeviceAuth()
	if err != nil {
		writeV1Error(w, http.StatusBadGateway, err.Error())
		return
	}
	writeV1JSON(w, http.StatusOK, resp)
}

func (s *Server) v1TidalDevicePoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.DeviceCode == "" {
		writeV1Error(w, http.StatusBadRequest, "device_code required")
		return
	}
	result := backend.PollTidalDeviceAuth(req.DeviceCode)
	writeV1JSON(w, http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// API Status
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1APIStatus(w http.ResponseWriter, r *http.Request) {
	// Serve from cache if still fresh
	if cached, ok := getCachedStatuses(); ok {
		writeV1JSON(w, http.StatusOK, cached)
		return
	}

	// Read optional configured URLs from settings
	spotFetchURL := ""
	if settings, err := s.app.LoadSettings(); err == nil && settings != nil {
		if useAPI, _ := settings["useSpotFetchAPI"].(bool); useAPI {
			if u, _ := settings["spotFetchAPIUrl"].(string); u != "" {
				spotFetchURL = u
			}
		}
	}

	results := CheckAllServices(jellyfinURL, spotFetchURL)
	setCachedStatuses(results)
	writeV1JSON(w, http.StatusOK, results)
}

// ─────────────────────────────────────────────────────────────────────────────
// Proxy config
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1GetProxies(w http.ResponseWriter, r *http.Request) {
	writeV1JSON(w, http.StatusOK, GetProxyConfig(s.ctr.DB))
}

func (s *Server) v1PutProxies(w http.ResponseWriter, r *http.Request) {
	var cfg ProxyConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := SaveProxyConfig(s.ctr.DB, cfg); err != nil {
		writeV1Error(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

