package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// ─────────────────────────────────────────────────────────────────────────────
// Constants
// ─────────────────────────────────────────────────────────────────────────────

const (
	discoveryInterval = 6 * time.Hour
	tidalUptimeURL    = "https://tidal-uptime.geeked.wtf"
	maxDiscoveryAge   = 24 * time.Hour
)

var (
	bucketDiscovery    = []byte("proxy_discovery")
	keyDiscoveryResult = []byte("last_result")
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

// ProxyDiscoveryResult holds the outcome of a single discovery run.
// Persisted in BoltDB so the effective proxy list is immediately correct
// after a server restart (before the next scheduled run).
type ProxyDiscoveryResult struct {
	CheckedAt   int64    `json:"checked_at"`
	NextCheckAt int64    `json:"next_check_at"`
	TidalUp     []string `json:"tidal_up"`    // proxies confirmed up by the monitor
	TidalDown   []string `json:"tidal_down"`  // proxies confirmed down
	TidalAdded  []string `json:"tidal_added"` // newly discovered (not in user config)
	Source      string   `json:"source"`
	Error       string   `json:"error,omitempty"`
}

// tidalUptimeEntry is one entry from tidal-uptime.geeked.wtf JSON response.
type tidalUptimeEntry struct {
	URL     string `json:"url"`
	Version string `json:"version,omitempty"`
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

// tidalUptimeResponse is the top-level JSON from tidal-uptime.geeked.wtf.
type tidalUptimeResponse struct {
	LastUpdated string             `json:"lastUpdated"`
	API         []tidalUptimeEntry `json:"api"`
	Streaming   []tidalUptimeEntry `json:"streaming"`
	Down        []tidalUptimeEntry `json:"down"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Goroutine entry point
// ─────────────────────────────────────────────────────────────────────────────

// startProxyDiscovery launches the background proxy discovery goroutine.
// It runs an initial check after a short random jitter (to avoid thundering
// herd when multiple instances restart simultaneously), then on a 6-hour ticker.
// The goroutine exits cleanly when ctx is cancelled (server shutdown).
func startProxyDiscovery(ctx context.Context, db *bolt.DB) {
	// Jitter 0–30 s — prevents all instances from hitting tidal-uptime at the same time.
	jitter := time.Duration(rand.Intn(30)) * time.Second
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	// First run immediately after jitter.
	runDiscoveryOnce(ctx, db)

	ticker := time.NewTicker(discoveryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("[Discovery] Goroutine stopped.")
			return
		case <-ticker.C:
			runDiscoveryOnce(ctx, db)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Single discovery run
// ─────────────────────────────────────────────────────────────────────────────

func runDiscoveryOnce(ctx context.Context, db *bolt.DB) {
	fmt.Println("[Discovery] Running proxy discovery...")

	result := ProxyDiscoveryResult{
		CheckedAt:   time.Now().Unix(),
		NextCheckAt: time.Now().Add(discoveryInterval).Unix(),
		Source:      "tidal-uptime.geeked.wtf",
	}

	up, down, err := fetchTidalUptimeProxies(ctx)
	if err != nil {
		fmt.Printf("[Discovery] Failed to fetch tidal-uptime: %v\n", err)
		result.Error = err.Error()
		saveDiscoveryResult(db, result)
		return
	}

	result.TidalUp = up
	result.TidalDown = down

	// Compute which discovered proxies are not already in the user's configured list.
	normalize := func(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }
	current := util.GetTidalProxies()
	configSet := make(map[string]struct{}, len(current))
	for _, u := range current {
		configSet[normalize(u)] = struct{}{}
	}
	for _, u := range up {
		if _, exists := configSet[normalize(u)]; !exists {
			result.TidalAdded = append(result.TidalAdded, u)
		}
	}

	// Apply to the in-memory effective list (never touches BoltDB user config).
	util.SetTidalDiscovery(up, down)

	fmt.Printf("[Discovery] Tidal: %d up, %d down, %d newly discovered\n",
		len(up), len(down), len(result.TidalAdded))

	saveDiscoveryResult(db, result)
}

// ─────────────────────────────────────────────────────────────────────────────
// Fetch
// ─────────────────────────────────────────────────────────────────────────────

func fetchTidalUptimeProxies(ctx context.Context) (up, down []string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tidalUptimeURL, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "SpotiFLAC-Discovery/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("tidal-uptime returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, nil, err
	}

	var payload tidalUptimeResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, nil, fmt.Errorf("failed to parse response: %w", err)
	}

	normalize := func(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }
	seen := make(map[string]struct{})

	// Entries come from a third-party feed (tidal-uptime.geeked.wtf) that
	// isn't under our control. Their URLs get stored and later used
	// unattended as the base of outbound download requests
	// (backend/tidal/client.go), so a compromised or DNS-hijacked feed
	// could otherwise redirect the server at internal/loopback targets
	// (SSRF) with zero operator interaction. Drop anything that doesn't
	// look like a safe external http(s) endpoint before it ever reaches
	// util.SetTidalDiscovery / BoltDB.
	addValid := func(dst *[]string, rawURL string) {
		n := normalize(rawURL)
		if n == "" {
			return
		}
		if err := ValidateExternalURL(rawURL); err != nil {
			fmt.Printf("[Discovery] Ignoring unsafe proxy URL from tidal-uptime: %v\n", err)
			return
		}
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
			*dst = append(*dst, rawURL)
		}
	}

	// streaming[] comes first (confirmed full streaming), then api[] (server up).
	// In practice streaming[] is currently empty, but the ordering is future-proof.
	for _, entry := range payload.Streaming {
		addValid(&up, entry.URL)
	}
	for _, entry := range payload.API {
		addValid(&up, entry.URL)
	}
	for _, entry := range payload.Down {
		n := normalize(entry.URL)
		if n == "" {
			continue
		}
		down = append(down, entry.URL)
	}

	return up, down, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// BoltDB persistence
// ─────────────────────────────────────────────────────────────────────────────

func saveDiscoveryResult(db *bolt.DB, r ProxyDiscoveryResult) {
	if db == nil {
		return
	}
	data, err := json.Marshal(r)
	if err != nil {
		fmt.Printf("[Discovery] Failed to marshal result: %v\n", err)
		return
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketDiscovery)
		if err != nil {
			return err
		}
		return b.Put(keyDiscoveryResult, data)
	}); err != nil {
		fmt.Printf("[Discovery] Failed to save result to BoltDB: %v\n", err)
	}
}

// LoadLastDiscoveryResult reads the most recent discovery result from BoltDB.
// Returns nil, nil if no result has been persisted yet.
// Exported so the /apis/proxies handler can include it in the response.
func LoadLastDiscoveryResult(db *bolt.DB) (*ProxyDiscoveryResult, error) {
	if db == nil {
		return nil, nil
	}
	var result ProxyDiscoveryResult
	err := db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketDiscovery)
		if b == nil {
			return nil
		}
		v := b.Get(keyDiscoveryResult)
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &result)
	})
	if err != nil {
		return nil, err
	}
	if result.CheckedAt == 0 {
		return nil, nil // bucket exists but nothing saved yet
	}
	return &result, nil
}

// loadSavedDiscovery restores the last discovery result at startup so that
// GetTidalProxiesEffective() works correctly before the first scheduled run.
// Called from main.go immediately after LoadProxyConfig.
func loadSavedDiscovery(db *bolt.DB) {
	result, err := LoadLastDiscoveryResult(db)
	if err != nil || result == nil {
		return
	}
	if time.Since(time.Unix(result.CheckedAt, 0)) > maxDiscoveryAge {
		fmt.Printf("[Discovery] Cached result is stale (%s old), skipping restore\n",
			time.Since(time.Unix(result.CheckedAt, 0)).Round(time.Minute))
		return
	}
	util.SetTidalDiscovery(result.TidalUp, result.TidalDown)
	fmt.Printf("[Discovery] Restored: %d up, %d down (checked %s ago)\n",
		len(result.TidalUp), len(result.TidalDown),
		time.Since(time.Unix(result.CheckedAt, 0)).Round(time.Minute))
}
