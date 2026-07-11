package songlink

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type retryRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f retryRoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func makeSongLinkResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// TestGetAllURLsFromSpotifyRetriesOnServerError is the regression test for
// N11: maxRetries used to be decorative — every branch inside the loop
// either returned immediately or unconditionally broke after the first
// iteration, so a single transient 500 permanently failed ISRC/URL
// resolution despite the code appearing to allow 3 attempts. This asserts
// the call actually succeeds after two 500s, which would have failed
// outright before the fix.
func TestGetAllURLsFromSpotifyRetriesOnServerError(t *testing.T) {
	successBody, _ := json.Marshal(map[string]interface{}{
		"linksByPlatform": map[string]interface{}{
			"tidal": map[string]string{"url": "https://tidal.com/track/1"},
		},
	})

	var callCount int32
	transport := retryRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&callCount, 1)
		if n < 3 {
			return makeSongLinkResp(500, "Internal Server Error"), nil
		}
		return makeSongLinkResp(200, string(successBody)), nil
	})

	c := &SongLinkClient{client: &http.Client{Transport: transport}}
	urls, err := c.GetAllURLsFromSpotify("spotify-track-id", "")
	if err != nil {
		t.Fatalf("GetAllURLsFromSpotify: %v", err)
	}
	if urls.TidalURL != "https://tidal.com/track/1" {
		t.Errorf("TidalURL = %q, want the URL from the eventual successful response", urls.TidalURL)
	}
	if callCount != 3 {
		t.Errorf("callCount = %d, want 3 (2 failed attempts + 1 success)", callCount)
	}
}

// TestGetAllURLsFromSpotifyGivesUpAfterMaxRetries confirms it still fails
// (rather than retrying forever) once every attempt is exhausted.
func TestGetAllURLsFromSpotifyGivesUpAfterMaxRetries(t *testing.T) {
	var callCount int32
	transport := retryRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&callCount, 1)
		return makeSongLinkResp(500, "Internal Server Error"), nil
	})

	c := &SongLinkClient{client: &http.Client{Transport: transport}}
	_, err := c.GetAllURLsFromSpotify("spotify-track-id", "")
	if err == nil {
		t.Fatal("expected an error after exhausting all retries, got nil")
	}
	if callCount != 3 {
		t.Errorf("callCount = %d, want exactly 3 (maxRetries)", callCount)
	}
}

// TestGetAllURLsFromSpotifyDoesNotRetryOn429 confirms a 429 still bails
// immediately (and marks the client rate-limited) rather than retrying —
// only 5xx/network errors are worth a retry.
func TestGetAllURLsFromSpotifyDoesNotRetryOn429(t *testing.T) {
	var callCount int32
	transport := retryRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		atomic.AddInt32(&callCount, 1)
		return makeSongLinkResp(429, "Too Many Requests"), nil
	})

	c := &SongLinkClient{client: &http.Client{Transport: transport}}
	_, err := c.GetAllURLsFromSpotify("spotify-track-id", "")
	if err == nil {
		t.Fatal("expected an error on 429, got nil")
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want exactly 1 (no retry on 429)", callCount)
	}
	if !c.IsRateLimited() {
		t.Error("client should be marked rate-limited after a 429")
	}
}
