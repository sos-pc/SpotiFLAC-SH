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
//
// EMPTY since 2026-07-20, and that is a measurement, not an oversight.
// amzn.afkarxyz.fun → amazon.spotbye.qzz.io (May 2026) → both dead: the host
// no longer resolves at all. Upstream moved to amz-oss.spotbye.qzz.io, which
// is alive but only answers signed community-session requests (see
// docs/external-api-layer.md). Keeping a dead URL here bought nothing but a
// misleading "configured" list and one failed request per download attempt.
var amazonProxies = []string{}

// Deezer proxies (tried in order, first success wins)
var deezerProxies = []string{"https://api.deezmate.com"}

// qobuzMusicDLURL was the PRIMARY Qobuz provider (POST + AES-GCM derived
// X-Debug-Key), introduced by upstream in May 2026.
//
// EMPTY since 2026-07-20. Measured against the live service with a correctly
// derived key and a real Qobuz track id (8767428): HTTP 500, encrypted body.
// Without a key it answers 400 — so the endpoint does parse requests, it just
// never serves one. The probe that established this is kept as
// qobuz/musicdl_live_probe_test.go; set QOBUZ_LIVE_PROBE=<track id> to re-run
// it before ever putting this URL back.
var qobuzMusicDLURL = ""

// qobuzProviders holds GET-based Qobuz stream API base URLs, of the shape
//
//	{base}{trackID}&quality={q}   e.g. https://host/api/stream?trackId=
//
// Empty, and every known public instance is down — re-measured 2026-07-20:
//   - dab.yeet.su          → unreachable
//   - dabmusic.xyz         → 503, Cloudflare bot protection
//   - qbz.afkarxyz.qzz.io  → unreachable
//   - musicdl.me           → 500 (see above)
//
// The format is trivial, so ANY working instance revives Qobuz with zero code
// changes: add it via Settings → APIs → Proxy Configuration. That is currently
// the only path to Qobuz that does not require the community session.
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
