package main

// ─────────────────────────────────────────────────────────────────────────────
// REST API v1 — /api/v1/*
//
// Auth : JWT Bearer (frontend) ou X-API-Key (applications externes).
//
// Route registration is split into focused files:
//   api_auth.go       — handlers: login, me, API keys, Tidal auth, proxies
//   api_jobs.go       — routes: jobs queue, SSE stream, history
//   api_watchlists.go — routes: watchlists CRUD + sync
//   api_files.go      — routes: search, tracks, files, audio, media, system
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared response helpers
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

// ─────────────────────────────────────────────────────────────────────────────
// CORS middleware
// ─────────────────────────────────────────────────────────────────────────────

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
// Auth middleware
// ─────────────────────────────────────────────────────────────────────────────

// v1Auth wraps a handler with CORS + local bypass + JWT/API Key authentication.
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

// userIDFromContext returns the authenticated user's ID, or "" if anonymous.
func userIDFromContext(r *http.Request) string {
	if user := GetUserFromContext(r); user != nil {
		return user.UserID
	}
	return ""
}

// v1RequireAdmin returns 403 if the requesting user is not an admin.
func v1RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := GetUserFromContext(r)
	if user == nil || !user.IsAdmin {
		writeV1Error(w, http.StatusForbidden, "admin required")
		return false
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — top-level dispatcher
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerV1Routes() {
	// ── Auth & API Keys & Tidal & Proxies ─────────────────────────────────
	s.mux.Handle("POST /api/v1/auth/login", v1CORSMiddleware(http.HandlerFunc(s.v1Login)))
	s.mux.Handle("POST /api/v1/auth/local", v1CORSMiddleware(http.HandlerFunc(s.v1LocalLogin)))
	s.mux.Handle("GET /api/v1/auth/me", s.v1Auth(s.v1Me))
	s.mux.Handle("GET /api/v1/auth/keys", s.v1Auth(s.v1ListAPIKeys))
	s.mux.Handle("POST /api/v1/auth/keys", s.v1Auth(s.v1CreateAPIKey))
	s.mux.Handle("DELETE /api/v1/auth/keys/{id}", s.v1Auth(s.v1RevokeAPIKey))
	s.mux.Handle("GET /api/v1/auth/tidal/status", s.v1Auth(s.v1TidalStatus))
	s.mux.Handle("DELETE /api/v1/auth/tidal", s.v1Auth(s.v1TidalDisconnect))
	s.mux.Handle("POST /api/v1/auth/tidal/device/start", s.v1Auth(s.v1TidalDeviceStart))
	s.mux.Handle("POST /api/v1/auth/tidal/device/poll", s.v1Auth(s.v1TidalDevicePoll))
	s.mux.Handle("GET /api/v1/apis/status", s.v1Auth(s.v1APIStatus))
	s.mux.Handle("GET /api/v1/apis/proxies", s.v1Auth(s.v1GetProxies))
	s.mux.Handle("PUT /api/v1/apis/proxies", s.v1Auth(s.v1PutProxies))

	// ── Jobs, downloads, history ──────────────────────────────────────────
	s.registerJobRoutes()

	// ── Watchlists ────────────────────────────────────────────────────────
	s.registerWatchlistRoutes()

	// ── Search, tracks, files, audio, media, settings, system ────────────
	s.registerFileRoutes()

	// ── Admin maintenance ─────────────────────────────────────────────────
	s.registerAdminRoutes()
}

// v1LocalLogin handles POST /api/v1/auth/local — auto-login on direct LAN access.
func (s *Server) v1LocalLogin(w http.ResponseWriter, r *http.Request) {
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
}
