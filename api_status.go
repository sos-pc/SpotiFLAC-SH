package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend"
	"github.com/sos-pc/SpotiFLAC-SH/backend/meta"
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

// invalidateStatusCache lived here. Its only caller was SaveProxyConfig, which
// dropped the cache so a proxy edit showed up before the 30 s TTL expired. With
// no proxy configuration left there is nothing to invalidate on: every service
// the board probes is fixed at startup.

// ─────────────────────────────────────────────────────────────────────────────
// Health check helpers
// ─────────────────────────────────────────────────────────────────────────────

// ServiceStatus.Error is rendered by the UI in a one-line, truncating field
// (ApisTab.tsx), and it is read by someone asking "why is this service red?"
// — not by someone reading a stack trace. So these two helpers translate the
// two things that actually go wrong (we couldn't reach it / it answered with
// a code) into a short sentence whose MEANING comes first and whose technical
// detail comes second, where truncation can eat it harmlessly.
//
// The bar to clear used to be the Tidal PREVIEW message, which named the
// problem, the cause and the settings screen that fixed it. That probe went
// with the proxy list; the standard to hold these to did not.

// describeRequestError explains a transport-level failure. Raw Go errors leak
// here otherwise, e.g. `Get "https://x": dial tcp: lookup x: no such host`,
// which buries the one useful word ("host") in noise.
func describeRequestError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "Host not found — check the URL"
	}
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) {
		return "HTTPS certificate rejected — check the URL"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "No response — timed out"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "No response — timed out"
	}
	// Matched on text rather than errno on purpose: Windows reports socket
	// failures with Winsock wording AND its own numbering, so
	// errors.Is(err, syscall.ECONNREFUSED) is false there even for a plain
	// refused connection (verified — it silently fell through to the generic
	// message). Each case therefore lists the POSIX phrasing and the Windows
	// one.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"), // POSIX
		strings.Contains(msg, "actively refused"): // Windows (WSAECONNREFUSED)
		return "Connection refused — service may be stopped"
	case strings.Contains(msg, "connection reset"), // POSIX
		strings.Contains(msg, "forcibly closed"): // Windows (WSAECONNRESET)
		return "Connection dropped by the server"
	case strings.Contains(msg, "no route to host"),
		strings.Contains(msg, "network is unreachable"), // POSIX
		strings.Contains(msg, "unreachable network"):    // Windows (WSAENETUNREACH)
		return "Network unreachable — check your connection"
	}
	return "Could not reach the server"
}

// describeHTTPStatus explains a response code in terms of what it means for
// this service, rather than restating the number on its own ("HTTP 401" tells
// a user nothing they can act on). 429 is deliberately absent: callers surface
// it as Status "ratelimited", which the UI already colours and labels.
func describeHTTPStatus(code int) string {
	switch {
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return fmt.Sprintf("Access denied (HTTP %d) — credentials rejected", code)
	case code == http.StatusNotFound:
		return "Not found (HTTP 404) — check the URL"
	case code >= 500:
		return fmt.Sprintf("Service is failing (HTTP %d) — try again later", code)
	default:
		return fmt.Sprintf("Unexpected reply (HTTP %d)", code)
	}
}

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
			return ServiceStatus{Name: name, URL: url, Status: "down", Error: describeRequestError(err2), CheckedAt: time.Now().Unix()}
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
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: describeRequestError(err), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	if resp.StatusCode == 429 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	}
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: describeHTTPStatus(resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "Reply was cut off mid-transfer", CheckedAt: time.Now().Unix()}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "Reply was not valid JSON — service may have changed", CheckedAt: time.Now().Unix()}
	}

	trackName, _ := result["name"].(string)
	if trackName == "" {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: "Answered, but sent no track data — service is broken", CheckedAt: time.Now().Unix()}
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
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: describeRequestError(err), CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	latency := int(elapsed.Milliseconds())

	if resp.StatusCode == 429 {
		return ServiceStatus{Name: name, URL: baseURL, Status: "ratelimited", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	}
	if resp.StatusCode != http.StatusOK {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: describeHTTPStatus(resp.StatusCode), CheckedAt: time.Now().Unix()}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "Reply was cut off mid-transfer", CheckedAt: time.Now().Unix()}
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", Error: "Reply was not valid JSON — service may have changed", CheckedAt: time.Now().Unix()}
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
		return ServiceStatus{Name: name, URL: baseURL, Status: "down", LatencyMs: latency, Error: "Answered, but sent no track data — service is broken", CheckedAt: time.Now().Unix()}
	}

	return ServiceStatus{Name: name, URL: baseURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// pingAppleMusic validates the genre chain's top tier end to end: lift the web
// player token, then resolve a known ISRC and check a genre comes back.
//
// Worth checking rather than trusting, because this source has a failure mode
// the others don't: the token is scraped out of Apple's web player bundle, so
// if Apple restructures it, the source silently stops answering — forever, and
// with no error anyone would notice, since the chain just quietly falls through
// to Deezer's coarser album genres. This check is what turns that into a red
// dot next to the others.
func pingAppleMusic(name, _ string) ServiceStatus {
	const testISRC = "GBDUW0000053" // Daft Punk — One More Time
	const displayURL = "https://api.music.apple.com"

	start := time.Now()
	genre, _, err := meta.ResolveGenreFrom("apple", testISRC)
	latency := int(time.Since(start).Milliseconds())

	switch {
	case err != nil:
		return ServiceStatus{Name: name, URL: displayURL, Status: "down", LatencyMs: latency,
			Error: describeAppleGenreError(err), CheckedAt: time.Now().Unix()}
	case genre == "":
		// Token worked and Apple answered, it just has nothing for this ISRC —
		// which for a track this famous means something is off.
		return ServiceStatus{Name: name, URL: displayURL, Status: "down", LatencyMs: latency,
			Error: "Answered, but sent no genre for a known track", CheckedAt: time.Now().Unix()}
	}
	return ServiceStatus{Name: name, URL: displayURL, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// describeAppleGenreError keeps this source's one distinctive failure legible:
// "we can no longer find the token" is a code problem needing a human, not a
// network blip, and it should not read like one.
func describeAppleGenreError(err error) string {
	if strings.Contains(err.Error(), "no token in web player bundle") {
		return "Apple changed its web player — token lift needs fixing"
	}
	if strings.Contains(err.Error(), "HTTP 429") {
		return "Rate limited by Apple (HTTP 429)"
	}
	return describeRequestError(err)
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
	// Song.link was probed here until item 7b removed the last call to it. Deezer
	// stays: it is still the name-search ISRC fallback (ResolveByName).
	{"Deezer", "https://api.deezer.com", pingDeezer},
	// The genre chain's tiers, in the order it tries them (see meta/genre.go).
	{"Apple Music · genre", "https://api.music.apple.com", pingAppleMusic},
	{"MusicBrainz", "https://musicbrainz.org", nil},
	{"LRCLib", "https://lrclib.net", nil},
	{"Tidal API", "https://api.tidal.com", nil},
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckAllServices runs parallel health checks for every external service
// including Tidal/Qobuz proxies as currently configured by the user.
// User customisations made via Settings → APIs are reflected automatically.
// ─────────────────────────────────────────────────────────────────────────────

func CheckAllServices(jellyfinURL string, spotFetchURL string) []ServiceStatus {
	all := make([]serviceEntry, 0, 32)
	all = append(all, coreServices...)

	// One entry per Tidal community proxy used to be added here. That list is
	// gone (see api_auth.go): previews only, so it could not produce a download.
	// "Tidal API" in coreServices covers the endpoint that is actually used.

	// Download engine sidecar. Listed only when configured, so an install without
	// the engine doesn't show a phantom service that is always down. Its /health
	// answers 200 with no auth, so the generic pingURL is a real liveness signal
	// — and it's the one probe that says whether delegated providers can run at
	// all, since every ENGINE_SERVICES download goes through it.
	if engineURL := backend.EngineBaseURL(); engineURL != "" {
		all = append(all, serviceEntry{"Engine", strings.TrimRight(engineURL, "/") + "/health", pingURL})
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
			// Recovered per service: a bug in one checker must not crash
			// the whole process (an unrecovered panic in any goroutine
			// does, regardless of how deep this fan-out is), and must not
			// silently leave a blank entry in results either — report it
			// the same way every other checker failure is reported.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("[PANIC] recovered checking status", "service", s.name, "recover", r, "stack", string(debug.Stack()))
					results[idx] = ServiceStatus{Name: s.name, URL: s.url, Status: "down", Error: fmt.Sprintf("internal error: %v", r), CheckedAt: time.Now().Unix()}
				}
			}()
			check := s.checker
			if check == nil {
				check = pingURL
			}
			results[idx] = check(s.name, s.url)
		}(i, svc)
	}
	wg.Wait()

	return results
}
