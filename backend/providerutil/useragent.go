// Package providerutil holds logic shared by the download provider clients
// (backend/tidal, backend/qobuz, backend/amazon, backend/deezer) that used
// to be independently reimplemented in each one.
package providerutil

// ChromeUserAgent is the browser User-Agent string every provider client
// sends on its outbound requests (the community proxies/undocumented APIs
// these clients talk to generally expect a real browser UA). It was
// previously hardcoded identically in 12+ places across the 4 provider
// packages — one string to keep in sync (e.g. bumping the Chrome version)
// instead of N.
const ChromeUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
