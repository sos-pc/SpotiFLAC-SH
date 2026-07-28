package util

import (
	"sync"
)

// ─────────────────────────────────────────────────────────────────────────────
// Proxy configuration — package-level vars, safe for concurrent access.
// Defaults match the hardcoded values previously in each downloader.
// Main package calls Set* at startup and when the user saves changes.
// ─────────────────────────────────────────────────────────────────────────────

var proxyMu sync.RWMutex

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

// ─── Getters (used by downloaders) ───────────────────────────────────────────

func GetTidalProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(tidalProxies))
	copy(cp, tidalProxies)
	return cp
}

// GetTidalProxiesEffective lived here: a three-tier merge of the user list with
// an auto-discovery overlay (discovered-up first, then user proxies, then
// user proxies the feed had marked down). The feed is NXDOMAIN, so the overlay
// was always empty and the function always took its own "no discovery data"
// early return — i.e. it always returned exactly GetTidalProxies(). Callers use
// that directly now.

// ─── Setters (called from main package) ──────────────────────────────────────

func SetTidalProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(proxies))
	copy(cp, proxies)
	tidalProxies = cp
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
