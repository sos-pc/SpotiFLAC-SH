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
// Verified by hand 2026-07-28 with the app's own probe track (441821360). The
// tidal-uptime feed that used to do this is dead and its client was removed
// (item 8 of docs/dead-code-removal-plan.md), so NOTHING PRUNES THIS LIST any
// more — re-probe when you touch it:
//
//	curl -s -o /dev/null -w '%{http_code} %{time_total}s'
//	  'https://<host>/track/?id=441821360&quality=HI_RES_LOSSLESS'
//
//	eu-central.monochrome.tf   200, v2.10, 0.33 s
//	us-west.monochrome.tf      200, v2.10, 0.42 s
//	api.monochrome.tf          200, v2.5,  0.29 s
//	monochrome-api.samidy.com  200, v2.3,  0.25 s
//	hifi-api.kennyy.com.br     dropped — DNS resolves, connection times out
//
// All four answer assetPresentation="PREVIEW" (30-second segments) without a
// personal Tidal Premium token (Device Code flow). Full FLAC requires
// authentication via Settings → Tidal Account; these are the API layer it rides on.
var tidalProxies = []string{
	"https://eu-central.monochrome.tf",
	"https://us-west.monochrome.tf",
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
		"https://api.monochrome.tf",
		"https://monochrome-api.samidy.com",
	}
}
