package util

import "sync"

// ─────────────────────────────────────────────────────────────────────────────
// Proxy configuration — package-level vars, safe for concurrent access.
// Defaults match the hardcoded values previously in each downloader.
// Main package calls Set* at startup and when the user saves changes.
// ─────────────────────────────────────────────────────────────────────────────

var proxyMu sync.RWMutex

// Tidal community proxies — all implement the Hi-Fi API interface:
//
//	GET {base}/track/?id={tidalID}&audioquality=LOSSLESS
//
// Sources: https://github.com/monochrome-music/monochrome/blob/main/INSTANCES.md
var tidalProxies = []string{
	// Official Monochrome instances
	"https://monochrome-api.samidy.com",
	"https://api.monochrome.tf",
	// Community — TypeScript rewrite (geeked.wtf)
	"https://hifi.geeked.wtf",
	// Community — Lucida / QQDL
	"https://wolf.qqdl.site",
	"https://maus.qqdl.site",
	"https://vogel.qqdl.site",
	"https://katze.qqdl.site",
	"https://hund.qqdl.site",
	// Community — Limited/No-Sub accounts
	"https://tidal.kinoplus.online",
}

// Amazon Music proxies (tried in order, first success wins)
var amazonProxies = []string{"https://amzn.afkarxyz.fun"}

// Deezer proxies (tried in order, first success wins)
var deezerProxies = []string{"https://api.deezmate.com"}

// Qobuz community providers (base URL prefix, appended with trackID)
var qobuzProviders = []string{
	"https://dab.yeet.su/api/stream?trackId=",
	"https://dabmusic.xyz/api/stream?trackId=",
	"https://qbz.afkarxyz.qzz.io/api/track/",
}

// ─── Getters (used by downloaders) ───────────────────────────────────────────

func GetTidalProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(tidalProxies))
	copy(cp, tidalProxies)
	return cp
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
