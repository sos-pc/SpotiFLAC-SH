package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The shim answers 200 with {"status":"error"} for a failed download (all routes
// exhausted), so treating HTTP 200 as success would silently report a missing
// file as a completed download — the worker would then try to ingest "".
func TestDownloadTreatsStatusErrorAsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(downloadResponse{Status: "error", Error: "no route succeeded"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL).Download(context.Background(), "https://open.spotify.com/track/x", []string{"deezer"}, "LOSSLESS", "/staging", true)
	if err == nil {
		t.Fatal("status:error must be an error, got nil")
	}
	if !strings.Contains(err.Error(), "no route succeeded") {
		t.Errorf("error should carry the engine's reason, got %v", err)
	}
}

// Guards the same trap from the other side: ok with an empty path is not a
// usable result and must not reach ingestion.
func TestDownloadRejectsOkWithoutFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(downloadResponse{Status: "ok", File: ""})
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL).Download(context.Background(), "u", []string{"deezer"}, "LOSSLESS", "/staging", true); err == nil {
		t.Fatal("ok with no file path must be an error")
	}
}

func TestDownloadSendsContractAndReturnsFile(t *testing.T) {
	var got downloadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(downloadResponse{Status: "ok", File: "/staging/ab/t.flac", Log: "line"})
	}))
	defer srv.Close()

	res, err := NewClient(srv.URL).Download(context.Background(), "spurl", []string{"deezer", "qobuz"}, "LOSSLESS", "/staging", true)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.File != "/staging/ab/t.flac" || res.Log != "line" {
		t.Errorf("unexpected result %+v", res)
	}
	// The service list is how a provider is pinned for this download; sending the
	// wrong one would silently fetch from another provider than the caller asked.
	if len(got.Services) != 2 || got.Services[0] != "deezer" {
		t.Errorf("services not forwarded verbatim: %+v", got.Services)
	}
	if got.SpotifyURL != "spurl" || got.OutDir != "/staging" {
		t.Errorf("request not forwarded: %+v", got)
	}
}

// allow_fallback carries the user's "Allow Quality Fallback" setting. Because
// the engine defaults it to true, dropping the field would silently ignore a
// user who turned it OFF and hand them a CD-quality file they explicitly
// refused — a wrong result that looks like a success, so nothing would surface it.
func TestDownloadForwardsAllowFallbackWhenDisabled(t *testing.T) {
	var got downloadRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(downloadResponse{Status: "ok", File: "/staging/ab/t.flac"})
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL).Download(context.Background(), "u", []string{"qobuz"}, "HI_RES_LOSSLESS", "/staging", false); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got.AllowFallback {
		t.Error("allow_fallback=false was not forwarded; the engine would silently downgrade the quality")
	}
	if got.Quality != "HI_RES_LOSSLESS" {
		t.Errorf("quality not forwarded verbatim: %q", got.Quality)
	}
}

func TestDownloadSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL).Download(context.Background(), "u", nil, "LOSSLESS", "/staging", true); err == nil {
		t.Fatal("HTTP 500 must be an error")
	}
}
