package main

import (
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsJobDownloadPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/jobs/abc123/download", true},
		{"/api/v1/jobs/abc-def-456/download", true},
		{"/api/v1/jobs//download", false},        // empty id
		{"/api/v1/jobs/abc/def/download", false}, // id contains a slash
		{"/api/v1/jobs/abc123/downloadx", false}, // wrong suffix
		{"/api/v1/jobs/stream", false},           // the SSE path, not a download
		{"/api/v1/jobs/abc123", false},           // missing /download
		{"/api/v1/history/downloads", false},
		{"/api/v1/jobs/abc123/download/extra", false},
	}
	for _, c := range cases {
		if got := isJobDownloadPath(c.path); got != c.want {
			t.Errorf("isJobDownloadPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestV1AuthAcceptsStreamTokenOnJobDownload is the regression test for F1:
// the browser-triggered download link used to embed the full 24h session
// JWT in its URL because a "stream"-scoped token (see GenerateStreamToken)
// was rejected on every path except the SSE endpoints. This confirms a
// stream-scoped token is now accepted on the job-download endpoint, and
// still rejected everywhere else.
func TestV1AuthAcceptsStreamTokenOnJobDownload(t *testing.T) {
	s := &Server{ctr: &Container{}}
	next := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
	handler := s.v1Auth(next)

	token, err := auth.GenerateStreamToken(&auth.JWTClaims{UserID: "user1", IsAdmin: false})
	if err != nil {
		t.Fatalf("auth.GenerateStreamToken: %v", err)
	}

	t.Run("job download path -> accepted", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/job-123/download?token="+token, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("SSE jobs stream path -> still accepted", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream?token="+token, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
		}
	})

	t.Run("unrelated path -> still rejected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/history/downloads?token="+token, nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})
}

// TestGenerateStreamTokenPreservesTokenVersion is the regression test for a
// bug found in production: GenerateStreamToken built its derived JWTClaims
// from scratch and never copied TokenVersion from the originating session,
// so every stream token was minted with TokenVersion=0 regardless of the
// account's real value. v1Auth's live revocation check
// (profile.TokenVersion != claims.TokenVersion) then rejected every such
// token as "session revoked" the moment an account's TokenVersion was ever
// bumped above 0 (any Jellyfin admin-flag change) - SSE connections and
// job-download links looped on 401 forever, even for a fully valid,
// un-revoked session.
func TestGenerateStreamTokenPreservesTokenVersion(t *testing.T) {
	am := newTestAuthManager(t)
	s := &Server{ctr: &Container{Auth: am}}

	if _, err := am.GetOrCreateUser("u1", "Alice", true); err != nil {
		t.Fatalf("auth.GetOrCreateUser: %v", err)
	}
	// Bump TokenVersion above zero, matching any account that has ever had
	// a Jellyfin admin-flag change.
	if _, err := am.GetOrCreateUser("u1", "Alice", false); err != nil {
		t.Fatalf("auth.GetOrCreateUser (demote): %v", err)
	}
	profile, err := am.GetUser("u1")
	if err != nil {
		t.Fatalf("auth.GetUser: %v", err)
	}
	if profile.TokenVersion == 0 {
		t.Fatalf("test setup: TokenVersion should be > 0 after a privilege change")
	}

	sessionToken, err := auth.GenerateJWT(profile)
	if err != nil {
		t.Fatalf("auth.GenerateJWT: %v", err)
	}
	sessionClaims, err := auth.ValidateJWT(sessionToken)
	if err != nil {
		t.Fatalf("auth.ValidateJWT: %v", err)
	}

	streamToken, err := auth.GenerateStreamToken(sessionClaims)
	if err != nil {
		t.Fatalf("auth.GenerateStreamToken: %v", err)
	}

	handler := s.v1Auth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream?token="+streamToken, nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (stream token should not be treated as revoked) — body: %s", w.Code, http.StatusOK, w.Body.String())
	}
}
