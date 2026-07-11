package main

import (
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

	token, err := GenerateStreamToken(&JWTClaims{UserID: "user1", IsAdmin: false})
	if err != nil {
		t.Fatalf("GenerateStreamToken: %v", err)
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
