package songlink

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// itunesTestServer builds a fake iTunes Search API server that returns the
// provided results for any request.
func itunesTestServer(t *testing.T, results []itunesResult) (*httptest.Server, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		resp := struct {
			ResultCount int            `json:"resultCount"`
			Results     []itunesResult `json:"results"`
		}{
			ResultCount: len(results),
			Results:     results,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	srv := httptest.NewServer(mux)
	return srv, srv.Close
}

// newClientWithRedirect creates a SongLinkClient whose HTTP transport rewrites
// all itunes.apple.com requests to the given test-server base URL.
func newClientWithRedirect(baseURL string) *SongLinkClient {
	transport := &redirectTransport{base: baseURL}
	return &SongLinkClient{
		client: &http.Client{Transport: transport},
	}
}

// redirectTransport rewrites itunes.apple.com/search → testServer/search.
type redirectTransport struct {
	base string
	rt   http.RoundTripper
}

func (t *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt := t.rt
	if rt == nil {
		rt = http.DefaultTransport
	}
	if strings.Contains(req.URL.Host, "itunes.apple.com") {
		newURL := fmt.Sprintf("%s%s?%s", t.base, req.URL.Path, req.URL.RawQuery)
		newReq, err := http.NewRequest(req.Method, newURL, req.Body)
		if err != nil {
			return nil, err
		}
		newReq.Header = req.Header
		return rt.RoundTrip(newReq)
	}
	return rt.RoundTrip(req)
}

// ─── Tests ───────────────────────────────────────────────────────────────────

func TestSearchITunes_ExactDurationMatch(t *testing.T) {
	results := []itunesResult{
		{TrackID: 111, TrackTimeMillis: 210000, IsStreamable: true},
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a result, got nil")
	}
	if got.TrackID != 111 {
		t.Errorf("expected TrackID 111, got %d", got.TrackID)
	}
}

func TestSearchITunes_WithinTolerancePlus(t *testing.T) {
	// +2s within the ±3s tolerance
	results := []itunesResult{
		{TrackID: 222, TrackTimeMillis: 212000, IsStreamable: true},
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.TrackID != 222 {
		t.Errorf("expected TrackID 222, got %v", got)
	}
}

func TestSearchITunes_OutsideTolerance(t *testing.T) {
	// +5s outside ±3s tolerance → must return an error (no match)
	results := []itunesResult{
		{TrackID: 333, TrackTimeMillis: 215000, IsStreamable: true},
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err == nil {
		t.Errorf("expected error (outside tolerance), got result TrackID %d", got.TrackID)
	}
	if got != nil {
		t.Errorf("expected nil result on no-match, got TrackID %d", got.TrackID)
	}
}

func TestSearchITunes_PicksClosest(t *testing.T) {
	// Two results within tolerance; should pick the closer one (diff=500 vs diff=2000)
	results := []itunesResult{
		{TrackID: 400, TrackTimeMillis: 207000, IsStreamable: true}, // diff=3000 (edge, should work)
		{TrackID: 401, TrackTimeMillis: 210500, IsStreamable: true}, // diff=500 (closest)
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.TrackID != 401 {
		t.Errorf("expected closest result (TrackID 401), got %v", got)
	}
}

func TestSearchITunes_ZeroTrackIDSkipped(t *testing.T) {
	// Results with TrackID==0 must be ignored → no valid match → error
	results := []itunesResult{
		{TrackID: 0, TrackTimeMillis: 210000, IsStreamable: true},
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err == nil {
		t.Errorf("expected error when only TrackID==0 results, got %+v", got)
	}
}

func TestSearchITunes_EmptyResults(t *testing.T) {
	// No results at all → error
	srv, close := itunesTestServer(t, nil)
	defer close()

	c := newClientWithRedirect(srv.URL)
	_, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err == nil {
		t.Error("expected error on empty results, got nil")
	}
}

func TestSearchITunes_NoDuration_FirstNonZero(t *testing.T) {
	// When durationMs==0, should return first result with TrackID != 0
	results := []itunesResult{
		{TrackID: 0, TrackTimeMillis: 210000},
		{TrackID: 500, TrackTimeMillis: 999999},
	}
	srv, close := itunesTestServer(t, results)
	defer close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || got.TrackID != 500 {
		t.Errorf("expected TrackID 500, got %v", got)
	}
}

func TestSearchITunes_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newClientWithRedirect(srv.URL)
	got, err := c.searchITunes("Song", "Artist", "Album", 210000)
	if err == nil {
		t.Errorf("expected error on 500 response, got result %+v", got)
	}
}
