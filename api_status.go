package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/songlink"
	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type ServiceStatus struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"` // "ok" | "down" | "ratelimited" | "unconfigured"
	LatencyMs int    `json:"latency_ms,omitempty"`
	CheckedAt int64  `json:"checked_at"`
	Error     string `json:"error,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Status cache (TTL 30s)
// ─────────────────────────────────────────────────────────────────────────────

const statusCacheTTL = 30 * time.Second

var (
	statusCache    []ServiceStatus
	statusCacheMu  sync.Mutex
	statusCachedAt time.Time
)

func getCachedStatuses() ([]ServiceStatus, bool) {
	statusCacheMu.Lock()
	defer statusCacheMu.Unlock()
	if statusCache != nil && time.Since(statusCachedAt) < statusCacheTTL {
		return statusCache, true
	}
	return nil, false
}

func setCachedStatuses(s []ServiceStatus) {
	statusCacheMu.Lock()
	defer statusCacheMu.Unlock()
	statusCache = s
	statusCachedAt = time.Now()
}

func invalidateStatusCache() {
	statusCacheMu.Lock()
	defer statusCacheMu.Unlock()
	statusCache = nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Health check helpers
// ─────────────────────────────────────────────────────────────────────────────

// statusFromCode maps an HTTP status code to a service status string.
//   - 429       → "ratelimited"
//   - 4xx       → "ok"  (server is reachable; root URL may not exist for API-only services)
//   - 2xx / 3xx → "ok"
//   - 5xx       → "down"  (server error or unavailable)
func statusFromCode(code int) string {
	switch {
	case code == 429:
		return "ratelimited"
	case code >= 400 && code < 500:
		return "ok"
	case code >= 200 && code < 400:
		return "ok"
	default:
		return "down"
	}
}

func doRequest(ctx context.Context, method, url string) (*http.Response, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "SpotiFLAC-StatusCheck/1.0")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	return resp, time.Since(start), err
}

// pingURL checks whether a URL is reachable, interpreting the HTTP status code.
func pingURL(name, url string) ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodHead, url)
	if err != nil {
		// Some servers don't support HEAD — fall back to GET
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		resp2, elapsed2, err2 := doRequest(ctx2, http.MethodGet, url)
		if err2 != nil {
			return ServiceStatus{Name: name, URL: url, Status: "down", Error: err2.Error(), CheckedAt: time.Now().Unix()}
		}
		resp2.Body.Close()
		status := statusFromCode(resp2.StatusCode)
		errMsg := ""
		if status == "down" {
			errMsg = fmt.Sprintf("HTTP %d", resp2.StatusCode)
		}
		return ServiceStatus{Name: name, URL: url, Status: status, LatencyMs: int(elapsed2.Milliseconds()), Error: errMsg, CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	status := statusFromCode(resp.StatusCode)
	errMsg := ""
	if status == "down" {
		errMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return ServiceStatus{Name: name, URL: url, Status: status, LatencyMs: int(elapsed.Milliseconds()), Error: errMsg, CheckedAt: time.Now().Unix()}
}

// pingSpotFetch performs a real track lookup to validate SpotFetch is fully
// functional, not just reachable.
func pingSpotFetch(name, baseURL string) ServiceStatus {
	const testTrackID = "7qiZfU4dY1lWllzX7mPBI3" // Shape of You — Ed Sheeran
	testURL := strings.TrimSuffix(baseURL, "/") + "/track/" + testTrackID

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodGet, testURL)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	if resp.StatusCode == 429 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	}
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "failed to read response", CheckedAt: time.Now().Unix()}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "invalid JSON response", CheckedAt: time.Now().Unix()}
	}

	trackName, _ := result["name"].(string)
	if trackName == "" {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: "missing track name in response", CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// pingDeezerProxy validates a community Deezer download proxy by requesting
// a known track from the /dl/ endpoint used by the downloader.
// Unlike pingDeezer (which checks api.deezer.com), this tests community
// proxies that serve FLAC files directly.
func pingDeezerProxy(name, baseURL string) ServiceStatus {
	const testTrackID = "3135556" // Get Lucky — Daft Punk
	testURL := strings.TrimSuffix(baseURL, "/") + "/dl/" + testTrackID

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodGet, testURL)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	switch {
	case resp.StatusCode == 429:
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode >= 500:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode != http.StatusOK:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// pingTidalProxy performs a real track-endpoint request to validate that a
// community Tidal HiFi proxy is actually serving audio data — not just
// reachable at its root URL.
//
// Format used by all community proxies:
//
//	{baseURL}/info?id={testID}
//
// Interpretation (strict — 4xx are NOT "ok" for API endpoints):
//
//	200               → ok   (proxy accepted the request and returned data)
//	401 / 403         → down
//	429               → ratelimited
//	5xx / network err → down
func pingTidalProxy(name, baseURL string) ServiceStatus {
	// /info retourne les métadonnées du track sans demander de qualité.
	// Compatible Python hifi-api et TypeScript hifi-api-workers.
	// Évite les faux négatifs causés par le paramètre audioquality ignoré
	// par les proxies TypeScript (qui défaut vers HI_RES_LOSSLESS).
	const testTrackID = "190909076"
	testURL := strings.TrimSuffix(baseURL, "/") + "/info?id=" + testTrackID

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodGet, testURL)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	switch {
	case resp.StatusCode == 429:
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode >= 500:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode != http.StatusOK:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	// 200 — verify the body contains at least some JSON to rule out stub pages
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || len(body) < 2 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: "empty or unreadable response", CheckedAt: time.Now().Unix()}
	}
	var result map[string]interface{}
	if json.Unmarshal(body, &result) != nil || len(result) == 0 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: "non-JSON or empty JSON response", CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// pingQobuzProxy performs a real track-endpoint request to validate a
// community Qobuz proxy.  Two URL schemes are in use:
//
//	https://dab.yeet.su/api/stream?trackId={id}&quality=6
//	https://qbz.afkarxyz.qzz.io/api/track/{id}?quality=6
//
// The baseURL passed here is the API base (already contains the path prefix),
// matching what proxy_config.go stores.  The function appends the track ID
// using the same logic as qobuz.buildQobuzAPIURL.
func pingQobuzProxy(name, baseURL string) ServiceStatus {
	// Qobuz track ID 20882393 — "Get Lucky" by Daft Punk.
	const testTrackID = "20882393"

	var testURL string
	if strings.Contains(baseURL, "qbz.afkarxyz") {
		testURL = baseURL + testTrackID + "?quality=6"
	} else {
		testURL = baseURL + testTrackID + "&quality=6"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodGet, testURL)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	switch {
	case resp.StatusCode == 429:
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode >= 500:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	case resp.StatusCode != http.StatusOK:
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil || len(body) < 2 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: "empty or unreadable response", CheckedAt: time.Now().Unix()}
	}
	var result map[string]interface{}
	if json.Unmarshal(body, &result) != nil || len(result) == 0 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency,
			Error: "non-JSON or empty JSON response", CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// pingDeezer performs a real track lookup to validate the Deezer API is
// returning valid data (not just an HTTP 200 with an error payload).
func pingDeezer(name, baseURL string) ServiceStatus {
	const testTrackID = "3135556" // Get Lucky — Daft Punk
	testURL := strings.TrimSuffix(baseURL, "/") + "/track/" + testTrackID

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()

	resp, elapsed, err := doRequest(ctx, http.MethodGet, testURL)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	if resp.StatusCode == 429 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	}
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: fmt.Sprintf("HTTP %d", resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "failed to read response", CheckedAt: time.Now().Unix()}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "invalid JSON response", CheckedAt: time.Now().Unix()}
	}

	// Deezer wraps errors as {"error": {"type": "...", "message": "...", "code": N}}
	if errObj := result["error"]; errObj != nil {
		errMsg := "API error in response"
		if m, ok := errObj.(map[string]interface{}); ok {
			if msg, _ := m["message"].(string); msg != "" {
				errMsg = msg
			}
		}
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: errMsg, CheckedAt: time.Now().Unix()}
	}

	if result["id"] == nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: "missing track id in response", CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// ─────────────────────────────────────────────────────────────────────────────
// Services to check
// ─────────────────────────────────────────────────────────────────────────────

type serviceEntry struct {
	name    string
	url     string
	checker func(name, url string) ServiceStatus // nil → use pingURL
}

var coreServices = []serviceEntry{
	{"SongLink", "https://api.song.link", nil},
	{"Deezer", "https://api.deezer.com", pingDeezer},
	{"MusicBrainz", "https://musicbrainz.org", nil},
	{"LRCLib", "https://lrclib.net", nil},
	{"Tidal API", "https://api.tidal.com", nil},
}

// proxyDisplayName extracts a short human-readable label from a proxy base URL.
// "https://wolf.qqdl.site/track/?id=" → "wolf.qqdl.site"
func proxyDisplayName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		// Fallback: strip scheme manually
		s := strings.TrimPrefix(rawURL, "https://")
		s = strings.TrimPrefix(s, "http://")
		if idx := strings.IndexAny(s, "/?"); idx > 0 {
			return s[:idx]
		}
		return s
	}
	return u.Hostname()
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckAllServices runs parallel health checks for every external service
// including Tidal/Qobuz proxies as currently configured by the user.
// User customisations made via Settings → APIs are reflected automatically.
// ─────────────────────────────────────────────────────────────────────────────

func CheckAllServices(jellyfinURL string, spotFetchURL string) []ServiceStatus {
	all := make([]serviceEntry, 0, 32)
	all = append(all, coreServices...)

	// Build Tidal proxy entries from the live configuration so that any proxy
	// the user has added or removed in Settings is reflected here.
	for _, proxyURL := range util.GetTidalProxies() {
		name := "Tidal · " + proxyDisplayName(proxyURL)
		all = append(all, serviceEntry{name, proxyURL, pingTidalProxy})
	}

	// Build Qobuz entries from the live configuration.
	// The stored URL is the full API base (includes path prefix) as used by
	// the downloader — pingQobuzProxy appends the test track ID correctly.
	for _, proxyBase := range util.GetQobuzProviders() {
		name := "Qobuz · " + proxyDisplayName(proxyBase)
		all = append(all, serviceEntry{name, proxyBase, pingQobuzProxy})
	}

	// Amazon community proxies — read from live config.
	// pingURL is used (no standard test ASIN available for Amazon Music).
	for _, proxyURL := range util.GetAmazonProxies() {
		name := "Amazon · " + proxyDisplayName(proxyURL)
		all = append(all, serviceEntry{name, proxyURL, nil})
	}

	// Deezer community proxies — read from live config.
	// pingDeezerProxy tests the actual /dl/ endpoint with a known track ID.
	for _, proxyURL := range util.GetDeezerProxies() {
		name := "Deezer · " + proxyDisplayName(proxyURL)
		all = append(all, serviceEntry{name, proxyURL, pingDeezerProxy})
	}

	if jellyfinURL != "" {
		all = append(all, serviceEntry{"Jellyfin", jellyfinURL, nil})
	}
	if spotFetchURL != "" {
		all = append(all, serviceEntry{"SpotFetch", spotFetchURL, pingSpotFetch})
	}

	results := make([]ServiceStatus, len(all))
	var wg sync.WaitGroup
	for i, svc := range all {
		wg.Add(1)
		go func(idx int, s serviceEntry) {
			defer wg.Done()
			check := s.checker
			if check == nil {
				check = pingURL
			}
			results[idx] = check(s.name, s.url)
		}(i, svc)
	}
	wg.Wait()

	// Override SongLink status if rate-limited in memory
	sl := songlink.GetSongLinkClient()
	if sl.IsRateLimited() {
		for i, r := range results {
			if r.Name == "SongLink" {
				results[i].Status = "ratelimited"
				results[i].Error = "Rate limited — retry after " + sl.RateLimitedUntil().Format("15:04:05")
			}
		}
	}

	return results
}
