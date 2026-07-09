package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"public https", "https://eu-central.monochrome.tf", false},
		{"public http", "http://api.example.com/track", false},
		{"public https with port", "https://api.example.com:8443/x", false},
		{"non-http scheme", "ftp://example.com", true},
		{"file scheme", "file:///etc/passwd", true},
		{"missing host", "https:///path", true},
		{"invalid URL", "http://[::1", true},
		{"loopback IP literal", "http://127.0.0.1/track", true},
		{"loopback IPv6 literal", "http://[::1]/track", true},
		{"localhost hostname", "http://localhost/track", true},
		{"private 10.x literal", "http://10.0.0.5/track", true},
		{"private 192.168.x literal", "http://192.168.1.10/track", true},
		{"link-local / cloud metadata", "http://169.254.169.254/latest/meta-data", true},
		{"unspecified", "http://0.0.0.0/track", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateExternalURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

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
func requestWithClaims(claims *JWTClaims) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/jobs", nil)
	ctx := context.WithValue(r.Context(), contextKeyUser, claims)
	return r.WithContext(ctx)
}

func TestV1RequirePermission(t *testing.T) {
	tests := []struct {
		name   string
		claims *JWTClaims
		perm   string
		want   bool
	}{
		{"no claims", nil, "download", false},
		{"full session, no IsAPIKey flag, no explicit perms", &JWTClaims{UserID: "u1"}, "download", true},
		{"admin API key bypasses scope check", &JWTClaims{UserID: "u1", IsAPIKey: true, IsAdmin: true}, "download", true},
		{"API key with matching permission", &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read", "download"}}, "download", true},
		{"API key missing permission", &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: []string{"read"}}, "download", false},
		{"API key with empty permissions", &JWTClaims{UserID: "u1", IsAPIKey: true, Permissions: nil}, "download", false},
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

func TestGetOrCreateUserBumpsTokenVersionOnPrivilegeChange(t *testing.T) {
	am := newTestAuthManager(t)

	p1, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (create): %v", err)
	}
	if p1.TokenVersion != 0 {
		t.Fatalf("new user TokenVersion = %d, want 0", p1.TokenVersion)
	}

	// Re-sync with the same admin flag: no privilege change, no bump.
	p2, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (no change): %v", err)
	}
	if p2.TokenVersion != 0 {
		t.Fatalf("TokenVersion after unchanged re-sync = %d, want 0", p2.TokenVersion)
	}

	// Jellyfin promotes the user to admin: privilege change, must bump so
	// any JWT issued before this point (still carrying admin=false, or an
	// old admin=true from a prior promotion/demotion cycle) stops matching.
	p3, err := am.GetOrCreateUser("jf-1", "Alice", true)
	if err != nil {
		t.Fatalf("GetOrCreateUser (promote): %v", err)
	}
	if p3.TokenVersion != 1 {
		t.Fatalf("TokenVersion after promotion = %d, want 1", p3.TokenVersion)
	}

	// Demoted back: another privilege change, another bump.
	p4, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (demote): %v", err)
	}
	if p4.TokenVersion != 2 {
		t.Fatalf("TokenVersion after demotion = %d, want 2", p4.TokenVersion)
	}
}
