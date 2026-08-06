package main

import (
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestValidateExternalURL lived here until 2026-07-28. ValidateExternalURL
// guarded one thing: operator-supplied Tidal proxy URLs, which became the base
// of the server's own outbound requests. That list and its PUT endpoint are
// gone (see api_auth.go), so the guard had no caller and went with them.
//
// Nothing replaced it because nothing needs it: the two remaining
// user-configurable URLs are Jellyfin and SpotFetch, and Jellyfin is routinely
// on a private address — applying this check there would reject legitimate
// installs, not protect them.
func TestIsSubPath(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		target string
		want   bool
	}{
		{"same path", "/music", "/music", true},
		{"nested one level", "/music", "/music/playlist1", true},
		{"nested deep", "/music", "/music/a/b/c", true},
		{"sibling directory", "/music", "/music-backup", false},
		{"unrelated root", "/music", "/etc", false},
		{"parent of root", "/music", "/", false},
		{"dot-dot escape", "/music", "/music/../etc", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSubPath(tt.root, tt.target)
			if got != tt.want {
				t.Errorf("isSubPath(%q, %q) = %v, want %v", tt.root, tt.target, got, tt.want)
			}
		})
	}
}

func TestCleanLibraryPath(t *testing.T) {
	const root = "/music"
	tests := []struct {
		name    string
		p       string
		want    string
		wantErr bool
	}{
		{"root itself", "/music", "/music", false},
		{"nested file", "/music/Artist/track.flac", "/music/Artist/track.flac", false},
		{"nested deep", "/music/a/b/c.flac", "/music/a/b/c.flac", false},
		{"empty", "", "", true},
		{"relative path", "Artist/track.flac", "", true},
		{"outside root", "/etc/passwd", "", true},
		{"sibling directory", "/music-backup/x", "", true},
		{"dot-dot escape", "/music/../etc/passwd", "", true},
		{"dot-dot escape disguised as nested", "/music/Artist/../../etc/passwd", "", true},
		// A Windows browser joins a download's output_dir with "\" even when
		// the server is Linux; the confinement must fold those to "/" so the
		// path is recognized as under the root rather than a sibling.
		{"backslash-separated subfolder", `/music\Artist\Album`, "/music/Artist/Album", false},
		{"backslash traversal still rejected", `/music\..\etc\passwd`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanLibraryPath(root, tt.p)
			if (err != nil) != tt.wantErr {
				t.Fatalf("cleanLibraryPath(%q, %q) error = %v, wantErr %v", root, tt.p, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("cleanLibraryPath(%q, %q) = %q, want %q", root, tt.p, got, tt.want)
			}
		})
	}
}

func TestCleanLibraryPaths(t *testing.T) {
	const root = "/music"

	t.Run("all valid", func(t *testing.T) {
		got, err := cleanLibraryPaths(root, []string{"/music/a.flac", "/music/sub/b.flac"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 || got[0] != "/music/a.flac" || got[1] != "/music/sub/b.flac" {
			t.Errorf("got %v", got)
		}
	})

	t.Run("one outside root rejects the whole batch", func(t *testing.T) {
		_, err := cleanLibraryPaths(root, []string{"/music/a.flac", "/etc/passwd"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		got, err := cleanLibraryPaths(root, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}

func TestIsSameOriginRequest(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{"no origin header", "app.example.com", "", true},
		{"matching origin", "app.example.com", "https://app.example.com", true},
		{"matching origin with port", "app.example.com:6890", "http://app.example.com:6890", true},
		{"cross-origin attacker site", "app.example.com", "https://evil.example.net", false},
		{"malformed origin", "app.example.com", "not a url", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{Header: make(http.Header), Host: tt.host}
			if tt.origin != "" {
				r.Header.Set("Origin", tt.origin)
			}
			got := isSameOriginRequest(r)
			if got != tt.want {
				t.Errorf("isSameOriginRequest(host=%q, origin=%q) = %v, want %v", tt.host, tt.origin, got, tt.want)
			}
		})
	}
}

func TestRemoteIPIgnoresForwardedHeadersByDefault(t *testing.T) {
	t.Setenv("TRUST_PROXY_HEADERS", "")
	r := makeRequest("192.168.1.50:1234", "203.0.113.9", "")
	if got := remoteIP(r); got != "192.168.1.50" {
		t.Errorf("remoteIP with no TRUST_PROXY_HEADERS = %q, want direct peer IP", got)
	}
}

func TestRemoteIPTrustsForwardedHeadersWhenConfigured(t *testing.T) {
	t.Setenv("TRUST_PROXY_HEADERS", "true")
	r := makeRequest("192.168.1.50:1234", "203.0.113.9, 192.168.1.1", "")
	if got := remoteIP(r); got != "192.168.1.1" {
		t.Errorf("remoteIP with TRUST_PROXY_HEADERS=true = %q, want rightmost X-Forwarded-For entry", got)
	}
}

// requestWithClaims builds a request carrying claims the way v1Auth would
// after a successful JWT/API-key validation.
func requestWithClaims(claims *auth.JWTClaims) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

func TestV1RequirePermission(t *testing.T) {
	tests := []struct {
		name   string
		claims *auth.JWTClaims
		perm   string
		want   bool
	}{
		{"no claims", nil, "download", false},
		{"full session, no IsAPIKey flag, no explicit perms", &auth.JWTClaims{UserID: "u1"}, "download", true},
		{"admin API key bypasses scope check", &auth.JWTClaims{UserID: "u1", IsAPIKey: true, IsAdmin: true}, "download", true},
		{"API key with matching permission", &auth.JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read", "download"}}, "download", true},
		{"API key missing permission", &auth.JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read"}}, "download", false},
		{"API key with empty permissions", &auth.JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: nil}, "download", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := requestWithClaims(tt.claims)
			w := httptest.NewRecorder()
			got := v1RequirePermission(w, r, tt.perm)
			if got != tt.want {
				t.Errorf("v1RequirePermission(perm=%q) = %v, want %v", tt.perm, got, tt.want)
			}
			if !got && w.Code != http.StatusForbidden && w.Code != http.StatusUnauthorized {
				t.Errorf("expected 403/401 on denial, got %d", w.Code)
			}
		})
	}
}

// TestRequireAuthRejectsRevokedTokenVersion is the regression test for the
// gap found while auditing auth.go for other issues after the ID-healing
// fix: v1Auth compares the live TokenVersion to reject a JWT issued before
// a privilege change (see UserProfile.TokenVersion), but RequireAuth
// (guarding /api/upload) validated only the JWT signature and expiry —
// a demoted/disabled admin's existing token would keep working there up
// to its full 24h natural expiry, bypassing the "revoke now" mechanism
// that works everywhere else.
func TestRequireAuthRejectsRevokedTokenVersion(t *testing.T) {
	am := newTestAuthManager(t)
	s := &Server{ctr: &Container{Auth: am}}

	profile, err := am.GetOrCreateUser("u1", "Alice", true)
	if err != nil {
		t.Fatalf("auth.GetOrCreateUser: %v", err)
	}
	staleToken, err := auth.GenerateJWT(profile)
	if err != nil {
		t.Fatalf("auth.GenerateJWT: %v", err)
	}

	// Privilege change bumps TokenVersion — every JWT issued before this
	// point (including staleToken above) must stop working immediately.
	if _, err := am.GetOrCreateUser("u1", "Alice", false); err != nil {
		t.Fatalf("auth.GetOrCreateUser (demote): %v", err)
	}

	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handlerCalled = true })
	r := httptest.NewRequest(http.MethodPost, "/api/upload", nil)
	r.Header.Set("Authorization", "Bearer "+staleToken)
	w := httptest.NewRecorder()

	s.RequireAuth(next).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d — body: %s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
	if handlerCalled {
		t.Errorf("next handler should not run for a revoked token")
	}
}
