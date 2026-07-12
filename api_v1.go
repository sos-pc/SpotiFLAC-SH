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
	"net/url"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
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

// isSubPath reports whether target is root itself or a descendant of root,
// after cleaning both. Does not resolve symlinks.
func isSubPath(root, target string) bool {
	rootClean := filepath.Clean(root)
	targetClean := filepath.Clean(target)
	if rootClean == targetClean {
		return true
	}
	rel, err := filepath.Rel(rootClean, targetClean)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// libraryRoot returns the operator-configured music library root that every
// file-management and download operation must stay within — the same
// downloadPath setting, with the same default fallback, that buildOutputDir
// (jobs_helpers.go) already uses to place downloaded files. Centralized here
// so every caller that needs to confine a client-supplied path resolves the
// root the same way, instead of each recomputing its own copy.
func (s *Server) libraryRoot() string {
	if settings, err := s.app.LoadSettings(); err == nil && settings != nil {
		if root, _ := settings["downloadPath"].(string); root != "" {
			return filepath.Clean(root)
		}
	}
	return filepath.Clean(util.GetDefaultMusicPath())
}

// cleanLibraryPath validates that p is an absolute path confined to root —
// the root itself or one of its descendants — returning the cleaned path.
// Use this (via s.libraryRoot()) instead of cleanAbsPath for any path that
// arrives in a request body/query and is used for file-management or
// download I/O: cleanAbsPath alone only rejects relative paths, it does not
// stop a client from naming any absolute path on the host filesystem.
func cleanLibraryPath(root, p string) (string, error) {
	clean, err := cleanAbsPath(p)
	if err != nil {
		return "", err
	}
	if !isSubPath(root, clean) {
		return "", fmt.Errorf("path %q is outside the configured library root", p)
	}
	return clean, nil
}

// cleanLibraryPaths validates a slice of paths against root, returning the
// cleaned slice or the first error encountered.
func cleanLibraryPaths(root string, paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		c, err := cleanLibraryPath(root, p)
		if err != nil {
			return nil, fmt.Errorf("path[%d]: %w", i, err)
		}
		out[i] = c
	}
	return out, nil
}

// cleanUploadOrLibraryPath accepts either a path confined to root (see
// cleanLibraryPath) or the exact path of a not-yet-expired temp file this
// server itself created via handleUpload. Freshly uploaded files (drag-drop
// or file-picker uploads on the Audio Converter / Analyzer pages) live in
// the OS temp dir, outside the library root by design — this lets
// analyze/convert accept exactly the path this server just handed back to
// the uploading client, without reopening path confinement to arbitrary
// caller-supplied locations.
func (s *Server) cleanUploadOrLibraryPath(root, p string) (string, error) {
	if clean, err := cleanAbsPath(p); err == nil && s.uploads.contains(clean) {
		return clean, nil
	}
	return cleanLibraryPath(root, p)
}

// cleanUploadOrLibraryPaths is the slice form of cleanUploadOrLibraryPath.
func (s *Server) cleanUploadOrLibraryPaths(root string, paths []string) ([]string, error) {
	out := make([]string, len(paths))
	for i, p := range paths {
		c, err := s.cleanUploadOrLibraryPath(root, p)
		if err != nil {
			return nil, fmt.Errorf("path[%d]: %w", i, err)
		}
		out[i] = c
	}
	return out, nil
}

// isSameOriginRequest reports whether a browser-sent Origin header (when
// present) matches the Host this request was addressed to. Requests with no
// Origin header (same-origin navigations, curl, server-to-server calls)
// are treated as same-origin. Used to keep token-issuing endpoints from
// being reachable via a cross-origin fetch() from an untrusted page, since
// the wildcard CORS policy on this API would otherwise let any website read
// the response.
func isSameOriginRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
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

// streamScopedPaths lists the exact endpoints a "stream"-scoped token (see
// GenerateStreamToken) may be used against. Kept short and explicit rather
// than pattern-matched — this is the enforcement boundary that makes a
// leaked stream token harmless outside its intended use.
var streamScopedPaths = map[string]bool{
	"/api/v1/jobs/stream":   true,
	"/api/v1/search/stream": true,
}

// isJobDownloadPath reports whether path is a job-download endpoint
// (/api/v1/jobs/{id}/download). Parameterized by job ID, so it can't be
// listed in streamScopedPaths' exact-match map — this is the second (and
// only other) place a stream-scoped token is accepted: like EventSource, a
// browser-triggered <a href> download can't set an Authorization header, so
// without this the full 24h session JWT would have to go in the URL instead.
func isJobDownloadPath(path string) bool {
	const prefix = "/api/v1/jobs/"
	const suffix = "/download"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id != "" && !strings.Contains(id, "/")
}

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
				if claims.Scope == "stream" && !streamScopedPaths[r.URL.Path] && !isJobDownloadPath(r.URL.Path) {
					writeV1Error(w, http.StatusUnauthorized, "unauthorized")
					return
				}
				// JWTs are otherwise stateless and can't be revoked before
				// they expire (up to 24h). Comparing against the live
				// TokenVersion closes that gap for the one case that bumps
				// it today (a Jellyfin admin-flag change) without a DB read
				// on API-key auth (already checked live) or on lookup
				// failure (e.g. the local-admin bypass profile, which is
				// never persisted — treated as unrevocable, same as before
				// this check existed).
				if !claims.IsAPIKey && s.ctr.Auth != nil {
					if profile, err := s.ctr.Auth.GetUser(claims.UserID); err == nil && profile.TokenVersion != claims.TokenVersion {
						writeV1Error(w, http.StatusUnauthorized, "session revoked")
						return
					}
				}
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

// v1RequirePermission returns 403 unless the caller may perform perm
// ("read", "manage", "admin"). Permission scoping only applies to API keys
// (JWTClaims.IsAPIKey) — a full browser/local session always has full
// access to its own account, matching what CreateAPIKey's docstring and
// the Permissions field on APIKey already promise but weren't enforcing.
func v1RequirePermission(w http.ResponseWriter, r *http.Request, perm string) bool {
	user := GetUserFromContext(r)
	if user == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	if user.IsAdmin || !user.IsAPIKey {
		return true
	}
	for _, p := range user.Permissions {
		// "download" is the pre-rename name for "manage" (it covered only
		// triggering downloads; "manage" now also covers settings and
		// watchlist management) — keys created before the rename still
		// have it stored and must keep working.
		if p == perm || (perm == "manage" && p == "download") {
			return true
		}
	}
	writeV1Error(w, http.StatusForbidden, fmt.Sprintf("API key missing %q permission", perm))
	return false
}

// ─────────────────────────────────────────────────────────────────────────────
// Route registration — top-level dispatcher
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) registerV1Routes() {
	// ── Auth & API Keys & Tidal & Proxies ─────────────────────────────────
	s.mux.Handle("POST /api/v1/auth/login", v1CORSMiddleware(http.HandlerFunc(s.v1Login)))
	s.mux.Handle("POST /api/v1/auth/local", v1CORSMiddleware(http.HandlerFunc(s.v1LocalLogin)))
	s.mux.Handle("GET /api/v1/auth/me", s.v1Auth(s.v1Me))
	s.mux.Handle("GET /api/v1/auth/stream-token", s.v1Auth(s.v1StreamToken))
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
	// This endpoint mints an admin token from nothing but a "local" source
	// IP, and the API's CORS policy is wildcarded — without this check any
	// website open in a LAN browser could fetch() it cross-origin and read
	// back an admin JWT (a "simple request" needs no preflight since this
	// handler doesn't read the body). Reject anything that isn't same-origin.
	if !isSameOriginRequest(r) {
		writeV1Error(w, http.StatusForbidden, "cross-origin request not allowed")
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
