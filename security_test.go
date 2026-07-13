package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
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

// TestGetOrCreateUserHealsMissingID is the regression test for a real
// production bug: a profile persisted under BoltDB key jellyfinID whose
// JSON blob has ID="" baked in (e.g. from before the ID field existed, or
// any other historical write that lost it) stayed permanently ID="" —
// every subsequent login refreshed Name/DisplayName/IsAdmin/UpdatedAt but
// never re-derived ID from the lookup key itself. A real Jellyfin admin
// hit this: their session correctly showed is_admin=true, but any API key
// they created inherited UserID="" and ValidateAPIKey's GetUser("") always
// failed, silently downgrading every admin-scoped key to non-admin.
func TestGetOrCreateUserHealsMissingID(t *testing.T) {
	am := newTestAuthManager(t)

	corrupted, err := json.Marshal(UserProfile{
		ID:          "", // the bug: stored blob has no ID even though the BoltDB key does
		Name:        "jf-legacy",
		DisplayName: "Legacy Admin",
		IsAdmin:     true,
		Settings:    make(map[string]interface{}),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := am.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketUsers)
		return b.Put([]byte("jf-legacy"), corrupted)
	}); err != nil {
		t.Fatalf("seed corrupted profile: %v", err)
	}

	profile, err := am.GetOrCreateUser("jf-legacy", "Legacy Admin", true)
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if profile.ID != "jf-legacy" {
		t.Fatalf("GetOrCreateUser did not heal ID: got %q, want %q", profile.ID, "jf-legacy")
	}

	// The real-world symptom: ValidateAPIKey looks up the profile via
	// GetUser(found.UserID) — this must now succeed and report IsAdmin.
	healed, err := am.GetUser("jf-legacy")
	if err != nil {
		t.Fatalf("GetUser after healing: %v", err)
	}
	if !healed.IsAdmin {
		t.Fatalf("healed profile IsAdmin = false, want true")
	}
}
