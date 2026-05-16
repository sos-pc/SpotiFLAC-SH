package util

import (
	"strings"
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Proxy configuration — package-level vars, safe for concurrent access.
// Defaults match the hardcoded values previously in each downloader.
// Main package calls Set* at startup and when the user saves changes.
// ─────────────────────────────────────────────────────────────────────────────

var proxyMu sync.RWMutex

// tidalDiscoveredUp/Down are populated by the discovery goroutine (proxy_discovery.go).
// In-memory only — never written to BoltDB user config.
// Set via SetTidalDiscovery(); read via GetTidalProxiesEffective().
var tidalDiscoveredUp []string
var tidalDiscoveredDown []string

// Tidal community proxies — all implement the Hi-Fi API interface:
//
//	GET {base}/track/?id={tidalID}&quality={quality}
//
// Status checked via tidal-uptime.geeked.wtf (May 2026).
// NOTE: as of May 2026, ALL community proxies return assetPresentation="PREVIEW"
// (30-second segments) without a valid personal Tidal Premium token (Device Code flow).
// Full FLAC downloads require authentication via Settings → Tidal Account.
var tidalProxies = []string{
	// Monochrome instances — confirmed server-UP by tidal-uptime.geeked.wtf
	"https://eu-central.monochrome.tf",
	"https://us-west.monochrome.tf",
	"https://hifi-api.kennyy.com.br",
	"https://api.monochrome.tf",
	"https://monochrome-api.samidy.com",
}

// Amazon Music proxy (requires X-Debug-Key header — handled by the downloader).
// Updated from amzn.afkarxyz.fun → amazon.spotbye.qzz.io (domain change, May 2026).
var amazonProxies = []string{"https://amazon.spotbye.qzz.io"}

// Deezer proxies (tried in order, first success wins)
var deezerProxies = []string{"https://api.deezmate.com"}

// qobuzMusicDLURL is the PRIMARY Qobuz provider introduced by upstream in May 2026.
// It uses a POST endpoint with an X-Debug-Key header (AES-GCM derived key).
// Handled separately from the standard GET-based provider list — see qobuz/client.go.
var qobuzMusicDLURL = "https://www.musicdl.me/api/qobuz/download"

// qobuzProviders holds legacy GET-based Qobuz stream API base URLs (user-configurable).
// The primary provider musicdl.me is accessed via GetQobuzMusicDLURL().
// As of May 2026:
//   - dab.yeet.su    → network unreachable (DNS down)
//   - dabmusic.xyz   → Cloudflare bot protection (inaccessible to API clients)
//   - qbz.afkarxyz.qzz.io → removed by upstream, presumed down
//
// Add working self-hosted instances via Settings → APIs → Proxy Configuration.
var qobuzProviders = []string{}

// ─── Getters (used by downloaders) ───────────────────────────────────────────

func GetTidalProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(tidalProxies))
	copy(cp, tidalProxies)
	return cp
}

// SetTidalDiscovery updates the in-memory discovery overlay.
// Called exclusively by the proxy discovery goroutine.
// Never modifies tidalProxies (the user-configured list).
func SetTidalDiscovery(up, down []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	tidalDiscoveredUp = append([]string(nil), up...)
	tidalDiscoveredDown = append([]string(nil), down...)
}

// GetTidalProxiesEffective returns the prioritized proxy list used for downloads
// and status checks. It merges the user config with the discovery overlay:
//
//	Tier 1 — discovered-up (freshest working proxies, discovery order)
//	Tier 2 — user-configured proxies NOT in discovered-down (original order)
//	Tier 3 — user-configured proxies IN discovered-down (last resort)
//
// Falls back to GetTidalProxies() when no discovery data is available.
// The user-configured list (tidalProxies / BoltDB) is never modified.
func GetTidalProxiesEffective() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()

	if len(tidalDiscoveredUp) == 0 && len(tidalDiscoveredDown) == 0 {
		cp := make([]string, len(tidalProxies))
		copy(cp, tidalProxies)
		return cp
	}

	normalize := func(u string) string { return strings.TrimRight(strings.TrimSpace(u), "/") }

	downSet := make(map[string]struct{}, len(tidalDiscoveredDown))
	for _, u := range tidalDiscoveredDown {
		downSet[normalize(u)] = struct{}{}
	}

	seen := make(map[string]struct{})
	var result []string

	// Tier 1: all discovered-up (includes proxies not in user config)
	for _, u := range tidalDiscoveredUp {
		n := normalize(u)
		if _, ok := seen[n]; !ok {
			seen[n] = struct{}{}
			result = append(result, u)
		}
	}

	// Tier 2 & 3: user-configured proxies
	var lastResort []string
	for _, u := range tidalProxies {
		n := normalize(u)
		if _, ok := seen[n]; ok {
			continue // already in Tier 1
		}
		if _, isDown := downSet[n]; isDown {
			lastResort = append(lastResort, u)
			continue
		}
		seen[n] = struct{}{}
		result = append(result, u)
	}

	// Tier 3: confirmed-down proxies from user config (absolute last resort)
	result = append(result, lastResort...)

	return result
}

func GetAmazonProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(amazonProxies))
	copy(cp, amazonProxies)
	return cp
}

func GetDeezerProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(deezerProxies))
	copy(cp, deezerProxies)
	return cp
}

// ─── Setters (called from main package) ──────────────────────────────────────

func SetTidalProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(proxies))
	copy(cp, proxies)
	tidalProxies = cp
}

func SetAmazonProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(proxies))
	copy(cp, proxies)
	amazonProxies = cp
}

func SetDeezerProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(proxies))
	copy(cp, proxies)
	deezerProxies = cp
}

func GetQobuzProviders() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(qobuzProviders))
	copy(cp, qobuzProviders)
	return cp
}

func SetQobuzProviders(providers []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(providers))
	copy(cp, providers)
	qobuzProviders = cp
}

// GetQobuzMusicDLURL returns the musicdl.me primary Qobuz provider URL.
func GetQobuzMusicDLURL() string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return qobuzMusicDLURL
}

// SetQobuzMusicDLURL updates the musicdl.me URL at runtime (applied immediately).
func SetQobuzMusicDLURL(u string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if u != "" {
		qobuzMusicDLURL = u
	}
}

// ─── Factory defaults (immutable hardcoded values) ────────────────────────────
// Used by defaultProxyConfig() in api_proxies.go to enable true "reset to
// defaults" behaviour — independent of the current in-memory state which may
// have been overridden by a saved user configuration.

func GetDefaultTidalProxies() []string {
	return []string{
		"https://eu-central.monochrome.tf",
		"https://us-west.monochrome.tf",
		"https://hifi-api.kennyy.com.br",
		"https://api.monochrome.tf",
		"https://monochrome-api.samidy.com",
	}
}

func GetDefaultQobuzProviders() []string {
	// No working GET-based providers as of May 2026.
	// Primary provider musicdl.me is hardcoded — see GetQobuzMusicDLURL().
	return []string{}
}

func GetDefaultAmazonProxies() []string {
	return []string{"https://amazon.spotbye.qzz.io"}
}

func GetDefaultDeezerProxies() []string {
	return []string{"https://api.deezmate.com"}
}
