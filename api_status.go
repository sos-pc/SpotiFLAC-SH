package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type ServiceStatus struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Status    string `json:"status"`    // "ok" | "down" | "ratelimited" | "unconfigured"
	LatencyMs int    `json:"latency_ms,omitempty"`
	CheckedAt int64  `json:"checked_at"`
	Error     string `json:"error,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Status cache (TTL 30s)
// ─────────────────────────────────────────────────────────────────────────────

const statusCacheTTL = 30 * time.Second

var (
	statusCache      []ServiceStatus
	statusCacheMu    sync.Mutex
	statusCachedAt   time.Time
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

func pingURL(name, url string) ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ServiceStatus{Name: name, URL: url, Status: "down", Error: err.Error(), CheckedAt: time.Now().Unix()}
	}
	req.Header.Set("User-Agent", "SpotiFLAC-StatusCheck/1.0")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := int(time.Since(start).Milliseconds())

	if err != nil {
		// Some servers don't support HEAD, try GET
		ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel2()
		req2, _ := http.NewRequestWithContext(ctx2, http.MethodGet, url, nil)
		req2.Header.Set("User-Agent", "SpotiFLAC-StatusCheck/1.0")
		start2 := time.Now()
		resp2, err2 := http.DefaultClient.Do(req2)
		latency = int(time.Since(start2).Milliseconds())
		if err2 != nil {
			return ServiceStatus{Name: name, URL: url, Status: "down", Error: err2.Error(), CheckedAt: time.Now().Unix()}
		}
		resp2.Body.Close()
		return ServiceStatus{Name: name, URL: url, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
	}
	defer resp.Body.Close()

	// Any HTTP response (even 4xx) means the server is reachable
	return ServiceStatus{Name: name, URL: url, Status: "ok", LatencyMs: latency, CheckedAt: time.Now().Unix()}
}

// ─────────────────────────────────────────────────────────────────────────────
// Services to check
// ─────────────────────────────────────────────────────────────────────────────

type serviceEntry struct {
	name string
	url  string
}

var coreServices = []serviceEntry{
	{"SongLink", "https://api.song.link"},
	{"Deezer", "https://api.deezer.com"},
	{"Amazon (afkarxyz)", "https://amzn.afkarxyz.fun"},
	{"MusicBrainz", "https://musicbrainz.org"},
	{"LRCLib", "https://lrclib.net"},
	{"Tidal API", "https://api.tidal.com"},
}

var tidalProxies = []serviceEntry{
	{"Tidal · triton.squid.wtf", "https://triton.squid.wtf"},
	{"Tidal · spotisaver.net (1)", "https://hifi-one.spotisaver.net"},
	{"Tidal · spotisaver.net (2)", "https://hifi-two.spotisaver.net"},
	{"Tidal · monochrome.tf (ohio)", "https://ohio-1.monochrome.tf"},
	{"Tidal · monochrome.tf (sg)", "https://singapore-1.monochrome.tf"},
	{"Tidal · qqdl.site (wolf)", "https://wolf.qqdl.site"},
	{"Tidal · qqdl.site (maus)", "https://maus.qqdl.site"},
	{"Tidal · qqdl.site (vogel)", "https://vogel.qqdl.site"},
	{"Tidal · qqdl.site (katze)", "https://katze.qqdl.site"},
	{"Tidal · qqdl.site (hund)", "https://hund.qqdl.site"},
	{"Tidal · monochrome.tf (api)", "https://api.monochrome.tf"},
}

var qobuzProviders = []serviceEntry{
	{"Qobuz · dab.yeet.su", "https://dab.yeet.su"},
	{"Qobuz · dabmusic.xyz", "https://dabmusic.xyz"},
	{"Qobuz · afkarxyz.fun", "https://qbz.afkarxyz.fun"},
}

// ─────────────────────────────────────────────────────────────────────────────
// CheckAllServices runs parallel health checks for every external service
// ─────────────────────────────────────────────────────────────────────────────

func CheckAllServices(jellyfinURL string, spotFetchURL string) []ServiceStatus {
	all := make([]serviceEntry, 0, 32)
	all = append(all, coreServices...)
	all = append(all, tidalProxies...)
	all = append(all, qobuzProviders...)

	if jellyfinURL != "" {
		all = append(all, serviceEntry{"Jellyfin", jellyfinURL})
	}
	if spotFetchURL != "" {
		all = append(all, serviceEntry{"SpotFetch", spotFetchURL})
	}

	results := make([]ServiceStatus, len(all))
	var wg sync.WaitGroup
	for i, svc := range all {
		wg.Add(1)
		go func(idx int, s serviceEntry) {
			defer wg.Done()
			results[idx] = pingURL(s.name, s.url)
		}(i, svc)
	}
	wg.Wait()

	// Override SongLink status if rate-limited in memory
	sl := backend.GetSongLinkClient()
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
