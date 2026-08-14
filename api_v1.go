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
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/settings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Shared response helpers
// ─────────────────────────────────────────────────────────────────────────────

// decodeV1JSON reads and decodes r's JSON body into dst (a pointer, same
// contract as json.Decoder.Decode). On failure it writes the 400 response
// itself and returns false — every v1 handler with a JSON body follows
// "decode or return" immediately after, so callers just do:
//
//	var req SomeRequest
//	if !decodeV1JSON(w, r, &req) {
//		return
//	}
func decodeV1JSON(w http.ResponseWriter, r *http.Request, dst interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeV1Error(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// cleanAbsPath normalizes a filesystem path and rejects anything that is not
// absolute after cleaning (prevents ../traversal tricks and relative paths).
//
// Backslashes are folded to forward slashes first. Output paths in this app
// are server-side paths, but in a self-hosted web deployment the browser
// building a download's output_dir may run a different OS than the server:
// a Windows browser joins path segments with "\" (see joinPath in the
// frontend), producing e.g. "/home/nonroot/Music\Artist\Album" for a Linux
// server. Without this fold, filepath.Clean on Linux keeps "\" as a literal
// filename character, so the path reads as a sibling of the library root
// rather than a descendant and the confinement check below wrongly rejects
// it. This matches util.SanitizeFolderPath, which already converts "\"→"/"
// unconditionally as the app's path convention; on a Windows server
// filepath.Clean converts the slashes back, so this is a no-op there.
func cleanAbsPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is required")
	}
	clean := filepath.Clean(strings.ReplaceAll(p, "\\", "/"))
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

// libraryRootForUser returns the music library root that file-management and
// download operations must stay within, for userID: their own saved
// downloadPath override if they have one, else the operator's global
// config.json — see EffectiveDownloadSettings (R8). userID == "" always
// resolves to the global default, which is also what buildOutputDir
// (jobs_helpers.go) falls back to when placing downloaded files.
func (s *Server) libraryRootForUser(userID string) string {
	if root := settings.EffectiveDownloadSettings(s.ctr.Auth, userID).DownloadPath; root != "" {
		return filepath.Clean(root)
	}
	return filepath.Clean(util.GetDefaultMusicPath())
}

// libraryRoot returns the GLOBAL library root (the operator's config.json
// downloadPath, ignoring any per-user override). Use this only for
// operations that must see one canonical root across every user — currently
// just api_admin.go's library-wide maintenance walk (collectScanRoots). Any
// per-request path confinement check must use libraryRootFor instead, or an
// authenticated user's own downloadPath override would be silently ignored.
func (s *Server) libraryRoot() string {
	return s.libraryRootForUser("")
}

// libraryRootFor returns the library root that should confine THIS request:
// the authenticated caller's own downloadPath override if they have one,
// else the global default. Use this (via cleanLibraryPath) for any
// client-supplied path — see api_files.go's file-routes doc comment.
func (s *Server) libraryRootFor(r *http.Request) string {
	return s.libraryRootForUser(userIDFromContext(r))
}

// cleanLibraryPath validates that p is an absolute path confined to root —
// the root itself or one of its descendants — returning the cleaned path.
// Use this (via s.libraryRootFor(r) for a per-request path, or s.libraryRoot()
// for a library-wide operation) instead of cleanAbsPath for any path that
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
		if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
			token = strings.TrimPrefix(authHeader, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != "" {
			if claims, err := auth.ValidateJWT(token); err == nil {
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
				ctx := auth.WithUser(r.Context(), claims)
				next(w, r.WithContext(ctx))
				return
			}
		}

		// 2. X-API-Key
		if apiKey := r.Header.Get("X-API-Key"); apiKey != "" && s.ctr.Auth != nil {
			if claims, ok := s.ctr.Auth.ValidateAPIKey(apiKey); ok {
				ctx := auth.WithUser(r.Context(), claims)
				next(w, r.WithContext(ctx))
				return
			}
		}

		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
	})))
}

// userIDFromContext returns the authenticated user's ID, or "" if anonymous.
func userIDFromContext(r *http.Request) string {
	if user := auth.GetUserFromContext(r); user != nil {
		return user.UserID
	}
	return ""
}

// v1RequireAdmin returns 403 if the requesting user is not an admin.
func v1RequireAdmin(w http.ResponseWriter, r *http.Request) bool {
	user := auth.GetUserFromContext(r)
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
	user := auth.GetUserFromContext(r)
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

// callerHasPermission is v1RequirePermission's membership rule with no HTTP
// response attached, for callers that need to *ask* rather than enforce —
// currently v1CreateAPIKey, which must refuse to mint a key stronger than the
// key requesting it.
//
// It lives here, next to v1RequirePermission, on purpose: the two must agree
// about what "holding a permission" means, including the "download" alias. If
// they ever drift, a key could be granted a permission it cannot itself use,
// or refused one it can.
func callerHasPermission(user *auth.JWTClaims, perm string) bool {
	if user == nil {
		return false
	}
	if user.IsAdmin || !user.IsAPIKey {
		return true
	}
	for _, p := range user.Permissions {
		if p == perm || (perm == "manage" && p == "download") {
			return true
		}
	}
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

	// ── Jobs, downloads, history ──────────────────────────────────────────
	s.registerJobRoutes()

	// ── Watchlists ────────────────────────────────────────────────────────
	s.registerWatchlistRoutes()

	// ── Search, tracks, files, audio, media, settings, system ────────────
	s.registerFileRoutes()

	// ── Admin maintenance ─────────────────────────────────────────────────
	s.registerSpotifyRoutes()
	s.registerAdminRoutes()

	// ── Admin read access to the SQLite catalog ───────────────────────────
	s.registerCatalogRoutes()

	// ── Admin closed write actions on the catalog ─────────────────────────
	s.registerCatalogActionRoutes()
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
	profile := &auth.UserProfile{ID: "local-admin", DisplayName: "Local Admin", IsAdmin: true}
	token, err := auth.GenerateJWT(profile)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]interface{}{
		"token": token,
		"user":  map[string]interface{}{"id": profile.ID, "display_name": profile.DisplayName, "is_admin": profile.IsAdmin},
	})
}

// jobVisibleTo reports whether user may see job in a PERSONAL view — the
// download queue and the events that feed it.
//
// The rule, written here rather than inlined at each site because three
// surfaces had drifted into three different answers: **a personal screen shows
// its owner their own work, administrator included.** Anything global belongs on
// an administration screen of its own, not mixed silently into this one.
//
// It used to exempt admins, so an operator's queue carried everyone's jobs with
// nothing saying whose — visible as another account's playlist appearing under
// their own downloads. Watchlists already filtered unconditionally, so the same
// account saw "my watchlists" beside "everybody's queue".
//
// A job with no UserID predates authentication and stays visible to all, until
// the backfill in the multi-user plan (§2c) gives those records an owner.
// Hiding them first would hide them from their own owner too.
func jobVisibleTo(user *auth.JWTClaims, jobUserID string) bool {
	if user == nil {
		return true // unauthenticated contexts have no one to filter against
	}
	if jobUserID == "" {
		return true
	}
	return jobUserID == user.UserID
}
