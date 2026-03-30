package backend

import "sync"

// ─────────────────────────────────────────────────────────────────────────────
// Proxy configuration — package-level vars, safe for concurrent access.
// Defaults match the hardcoded values previously in each downloader.
// Main package calls Set* at startup and when the user saves changes.
// ─────────────────────────────────────────────────────────────────────────────

var proxyMu sync.RWMutex

// Tidal community proxies
var tidalProxies = []string{
	"https://triton.squid.wtf",
	"https://hifi-one.spotisaver.net",
	"https://hifi-two.spotisaver.net",
	"https://ohio-1.monochrome.tf",
	"https://singapore-1.monochrome.tf",
	"https://wolf.qqdl.site",
	"https://maus.qqdl.site",
	"https://vogel.qqdl.site",
	"https://katze.qqdl.site",
	"https://hund.qqdl.site",
	"https://api.monochrome.tf",
}

// Amazon Music proxy base (without trailing slash or path)
var amazonProxyBase = "https://amzn.afkarxyz.fun"

// Deezer proxy base (without trailing slash or path)
var deezerProxyBase = "https://api.deezmate.com"

// ─── Getters (used by downloaders) ───────────────────────────────────────────

func GetTidalProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	cp := make([]string, len(tidalProxies))
	copy(cp, tidalProxies)
	return cp
}

func GetAmazonProxyBase() string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return amazonProxyBase
}

func GetDeezerProxyBase() string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return deezerProxyBase
}

// ─── Setters (called from main package) ──────────────────────────────────────

func SetTidalProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	cp := make([]string, len(proxies))
	copy(cp, proxies)
	tidalProxies = cp
}

func SetAmazonProxyBase(base string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	amazonProxyBase = base
}

func SetDeezerProxyBase(base string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	deezerProxyBase = base
}
