package main

// ─────────────────────────────────────────────────────────────────────────────
// Handlers — Auth, API Keys, Tidal auth, APIs status & proxies
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"net/http"

	"github.com/sos-pc/SpotiFLAC-SH/backend/tidal"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
)

// ─────────────────────────────────────────────────────────────────────────────
// Login / identity
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
	if !decodeV1JSON(w, r, &req) {
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
	token, err := auth.GenerateJWT(profile)
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

// v1StreamToken mints a short-lived, SSE-only token (see GenerateStreamToken)
// for the caller's already-authenticated identity. The frontend calls this
// right before opening an EventSource, since EventSource can't send an
// Authorization header — without this, the long-lived session JWT would
// have to go in the URL instead, where it can end up in reverse-proxy
// access logs or browser history for its full 24h lifetime.
func (s *Server) v1StreamToken(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetUserFromContext(r)
	if claims == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	token, err := auth.GenerateStreamToken(claims)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	writeV1JSON(w, http.StatusOK, map[string]interface{}{
		"token":      token,
		"expires_in": int(auth.StreamTokenTTL.Seconds()),
	})
}

func (s *Server) v1Me(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetUserFromContext(r)
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
// API Keys
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	// Read-only: key metadata for the caller's own account.
	if !v1RequirePermission(w, r, "read") {
		return
	}
	user := auth.GetUserFromContext(r)
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
		keys = []auth.APIKey{}
	}
	writeV1JSON(w, http.StatusOK, keys)
}

func (s *Server) v1CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUserFromContext(r)
	if user == nil {
		writeV1Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	if !decodeV1JSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeV1Error(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"read", "manage"}
	}
	// No caller may mint a key stronger than itself. Measured on prod
	// 2026-07-19: a read-only key could create a ["read","manage"] key and
	// then write settings with it — a permanent escalation, since API keys
	// never expire. The admin guard below did not catch it because "manage"
	// is not "admin", and it tests the *account* rather than the calling
	// credential.
	//
	// Only API keys are constrained. A browser session is the human
	// themselves, holding whatever their account allows; the admin check
	// below is what bounds them.
	if caller := auth.GetUserFromContext(r); caller != nil && caller.IsAPIKey {
		for _, want := range req.Permissions {
			if !callerHasPermission(caller, want) {
				writeV1Error(w, http.StatusForbidden,
					fmt.Sprintf("an API key cannot grant %q, a permission it does not hold", want))
				return
			}
		}
	}
	// A key inherits its owner's account, not the caller's session — without
	// this check, any authenticated non-admin user could self-issue a key
	// with "admin" permission and use it to reach every v1RequireAdmin-gated
	// endpoint indefinitely (API keys never expire, unlike JWTs).
	if !user.IsAdmin {
		for _, p := range req.Permissions {
			if p == "admin" {
				writeV1Error(w, http.StatusForbidden, "only an admin account can create a key with admin permission")
				return
			}
		}
	}
	rawKey, key, err := s.ctr.Auth.CreateAPIKey(user.UserID, req.Name, req.Permissions)
	if err != nil {
		writeV1Error(w, http.StatusInternalServerError, fmt.Sprintf("failed to create key: %v", err))
		return
	}
	writeV1JSON(w, http.StatusCreated, map[string]interface{}{
		"key":         rawKey,
		"id":          key.ID,
		"name":        key.Name,
		"permissions": key.Permissions,
		"created_at":  key.CreatedAt,
	})
}

func (s *Server) v1RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	// Destructive, though scoped to the caller's own account by RevokeAPIKey.
	// A read-only key has no business revoking credentials.
	if !v1RequirePermission(w, r, "manage") {
		return
	}
	user := auth.GetUserFromContext(r)
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
// Tidal auth (Device Code flow)
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1TidalStatus(w http.ResponseWriter, r *http.Request) {
	// Read-only: connection state of the instance's Tidal account.
	if !v1RequirePermission(w, r, "read") {
		return
	}
	token := tidal.LoadTidalToken()
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
	// Instance-wide: DeleteTidalToken clears a process-global token, so this cuts
	// Tidal for every user. Measured 2026-07-19: reachable by a read-only API key
	// before this guard (docs/api-redesign-plan.md phase 4).
	if !v1RequireAdmin(w, r) {
		return
	}
	tidal.DeleteTidalToken()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) v1TidalDeviceStart(w http.ResponseWriter, r *http.Request) {
	// Binds the instance's single Tidal account — same global resource as the
	// disconnect above.
	if !v1RequireAdmin(w, r) {
		return
	}
	resp, err := tidal.StartTidalDeviceAuth()
	if err != nil {
		writeV1Error(w, http.StatusBadGateway, err.Error())
		return
	}
	writeV1JSON(w, http.StatusOK, resp)
}

func (s *Server) v1TidalDevicePoll(w http.ResponseWriter, r *http.Request) {
	// Completes the binding and saves the token globally.
	if !v1RequireAdmin(w, r) {
		return
	}
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if !decodeV1JSON(w, r, &req) {
		return
	}
	if req.DeviceCode == "" {
		writeV1Error(w, http.StatusBadRequest, "device_code required")
		return
	}
	result := tidal.PollTidalDeviceAuth(req.DeviceCode)
	writeV1JSON(w, http.StatusOK, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// External API status & proxy config
// ─────────────────────────────────────────────────────────────────────────────

func (s *Server) v1APIStatus(w http.ResponseWriter, r *http.Request) {
	// Read-only: third-party service status.
	if !v1RequirePermission(w, r, "read") {
		return
	}
	if cached, ok := getCachedStatuses(); ok {
		writeV1JSON(w, http.StatusOK, cached)
		return
	}
	spotFetchURL := EffectiveDownloadSettings(s.ctr.Auth, userIDFromContext(r)).SpotFetchAPIURL
	results := CheckAllServices(auth.JellyfinURL, spotFetchURL)
	setCachedStatuses(results)
	writeV1JSON(w, http.StatusOK, results)
}

// GET and PUT /api/v1/apis/proxies lived here. They configured one list — the
// Tidal community HiFi proxies — which was removed on 2026-07-28 because every
// host on it answers assetPresentation="PREVIEW" without a personal token, and
// the download path refuses previews. A settings screen for a list that could
// not produce a download was worse than none.
//
// Tokenless Tidal goes through the engine now (ENGINE_SERVICES); with a token it
// goes to api.tidal.com. Neither is operator-configurable, and neither should be:
// letting an authenticated user repoint the base of the server's own outbound
// requests was an SSRF primitive that needed admin-only + ValidateExternalURL to
// contain. That surface is gone rather than guarded.
