package backend

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

const (
	tidalClientIDFallback = "CzET4vdadNUFQ5JU"
	tidalClientIDTTL      = 24 * time.Hour
	tidalBundleMaxBytes   = 15 * 1024 * 1024 // 15 MB
)

var (
	cachedClientID   string
	cachedClientIDAt time.Time
	clientIDMu       sync.Mutex
)

// GetTidalClientID retourne le client_id OAuth du Tidal Web Player.
// Il est scrapé depuis listen.tidal.com une fois toutes les 24h et mis en cache.
// En cas d'échec, la valeur de fallback est utilisée.
func GetTidalClientID() string {
	clientIDMu.Lock()
	defer clientIDMu.Unlock()

	if cachedClientID != "" && time.Since(cachedClientIDAt) < tidalClientIDTTL {
		return cachedClientID
	}

	id, err := scrapeTidalClientID()
	if err != nil || id == "" {
		fmt.Printf("[Tidal] client_id scraping failed (%v) — using fallback %s\n", err, tidalClientIDFallback)
		return tidalClientIDFallback
	}

	fmt.Printf("[Tidal] Scraped client_id: %s\n", id)
	cachedClientID = id
	cachedClientIDAt = time.Now()
	return id
}

// InvalidateTidalClientIDCache force le re-scraping au prochain appel.
func InvalidateTidalClientIDCache() {
	clientIDMu.Lock()
	cachedClientID = ""
	clientIDMu.Unlock()
}

func scrapeTidalClientID() (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	// ── Étape 1 : récupérer le HTML de listen.tidal.com ─────────────────────
	req, _ := http.NewRequest("GET", "https://listen.tidal.com", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch listen.tidal.com: %w", err)
	}
	defer resp.Body.Close()
	html, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return "", fmt.Errorf("read HTML: %w", err)
	}

	// ── Étape 2 : trouver l'URL du bundle JS principal ───────────────────────
	// Vite génère : <script type="module" crossorigin src="/assets/index-HASH.js">
	bundlePatterns := []*regexp.Regexp{
		regexp.MustCompile(`src="(/assets/index[^"]+\.js)"`),
		regexp.MustCompile(`src="(assets/index[^"]+\.js)"`),
		regexp.MustCompile(`src="(/assets/main[^"]+\.js)"`),
	}

	var bundleURL string
	for _, re := range bundlePatterns {
		if m := re.FindSubmatch(html); m != nil {
			path := string(m[1])
			if path[0] == '/' {
				bundleURL = "https://listen.tidal.com" + path
			} else {
				bundleURL = "https://listen.tidal.com/" + path
			}
			break
		}
	}
	if bundleURL == "" {
		return "", fmt.Errorf("JS bundle not found in listen.tidal.com HTML")
	}

	// ── Étape 3 : télécharger le bundle ─────────────────────────────────────
	req2, _ := http.NewRequest("GET", bundleURL, nil)
	req2.Header.Set("User-Agent", req.Header.Get("User-Agent"))
	resp2, err := client.Do(req2)
	if err != nil {
		return "", fmt.Errorf("fetch JS bundle: %w", err)
	}
	defer resp2.Body.Close()
	bundle, err := io.ReadAll(io.LimitReader(resp2.Body, tidalBundleMaxBytes))
	if err != nil {
		return "", fmt.Errorf("read JS bundle: %w", err)
	}

	// ── Étape 4 : extraire le client_id ─────────────────────────────────────
	// Patterns du plus spécifique au plus générique.
	// Les client_ids Tidal font 14-20 caractères alphanumériques.
	clientIDPatterns := []*regexp.Regexp{
		// Minifié sans espace : clientId:"CzET4vdadNUFQ5JU"
		regexp.MustCompile(`clientId:"([A-Za-z0-9]{14,20})"`),
		// Avec espace : clientId: "CzET4vdadNUFQ5JU"
		regexp.MustCompile(`clientId:\s*"([A-Za-z0-9]{14,20})"`),
		// Forme JSON : "clientId":"CzET4vdadNUFQ5JU"
		regexp.MustCompile(`"clientId"\s*:\s*"([A-Za-z0-9]{14,20})"`),
		// Snake case : client_id:"..."
		regexp.MustCompile(`client_id:\s*"([A-Za-z0-9]{14,20})"`),
	}

	for _, re := range clientIDPatterns {
		if m := re.FindSubmatch(bundle); m != nil {
			return string(m[1]), nil
		}
	}

	return "", fmt.Errorf("clientId pattern not found in JS bundle (%d bytes)", len(bundle))
}
