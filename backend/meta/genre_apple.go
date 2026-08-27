package meta

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// Apple Music is the most precise genre source we have: it answers on the
// ISRC itself (an exact catalog match, no name guessing) and returns a
// per-track, curated genre list — unlike Deezer, which only knows the album's
// genre, or MusicBrainz, whose tags are free-form folksonomy.
//
// It needs a bearer token. Apple's PUBLIC Web API would require a paid
// developer account, but the web player ships its own token in a JS bundle on
// a public page, which is what we lift here — the same move upstream makes for
// Qobuz (scraping app_id/app_secret out of the Qobuz web player bundle). These
// are the credentials of Apple's own public player, not a private secret.
//
// Measured before shipping: the token is valid 35 days, and the API absorbed
// 40 concurrent requests (~850 req/s) without a single 429 — unlike Spotify's
// public API, which rejects its web-player token outright.
//
// Two distinct failure modes, worth keeping apart:
//   - The token EXPIRES (~monthly). The API answers 401, we lift a fresh one
//     and retry. Self-healing; see appleMusicToken.
//   - Apple RESTRUCTURES the bundle. The regexes below find nothing and this
//     source is simply unavailable, forever, until someone fixes them. That is
//     the standing risk of any scraping, and why this source degrades into the
//     next tier of the chain instead of failing the caller — and why
//     pingAppleMusic reports it on the status page rather than letting it rot
//     silently.
const (
	appleBrowseURL    = "https://music.apple.com/us/browse"
	appleISRCEndpoint = "https://api.music.apple.com/v1/catalog/us/songs?filter[isrc]="
	appleUserAgent    = util.ChromeUserAgent
)

var (
	// Bundle names contain '~' (e.g. /assets/index~8eb1313596.js), which a
	// naive [A-Za-z0-9._/-] class silently misses.
	appleBundleRe = regexp.MustCompile(`/assets/[A-Za-z0-9._~/-]+\.js`)
	appleJWTRe    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`)

	appleTokenMu sync.Mutex
	appleToken   string
)

// appleGenreNames returns Apple's genres for an ISRC, most relevant first.
// A track that simply isn't in the US catalog yields an empty slice and no
// error — that is a miss, not a malfunction, and the chain moves on.
func appleGenreNames(isrc string) ([]string, error) {
	body, err := appleISRCLookup(isrc)
	if err != nil {
		return nil, err
	}

	var out struct {
		Data []struct {
			Attributes struct {
				GenreNames []string `json:"genreNames"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("apple: decode: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(out.Data[0].Attributes.GenreNames))
	for _, g := range out.Data[0].Attributes.GenreNames {
		// "Music" is Apple's root category and rides along on every track.
		if g == "" || g == "Music" {
			continue
		}
		names = append(names, g)
	}
	return names, nil
}

// appleISRCLookup performs the catalog request, refreshing the token once if
// Apple rejects it (the ~monthly expiry).
func appleISRCLookup(isrc string) ([]byte, error) {
	token, err := appleMusicToken(false)
	if err != nil {
		return nil, err
	}

	body, status, err := appleGet(appleISRCEndpoint+isrc, token)
	if err == nil {
		return body, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return nil, err
	}

	token, err = appleMusicToken(true)
	if err != nil {
		return nil, err
	}
	body, _, err = appleGet(appleISRCEndpoint+isrc, token)
	return body, err
}

func appleGet(url, token string) ([]byte, int, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", "https://music.apple.com")
	req.Header.Set("User-Agent", appleUserAgent)

	// Short: this call sits on the download's critical path (the genre channel
	// is awaited after the file lands), so it must fail fast, never hang.
	resp, err := util.NewHTTPClient(8 * time.Second).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("apple: HTTP %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

// appleMusicToken returns the cached web-player token, lifting a fresh one if
// absent or if refresh is demanded. The lock is deliberately held across the
// network work: a re-lift happens about once a month, and making concurrent
// callers wait for one lift is better than having them stampede Apple with
// identical bundle downloads.
func appleMusicToken(refresh bool) (string, error) {
	appleTokenMu.Lock()
	defer appleTokenMu.Unlock()

	if appleToken != "" && !refresh {
		return appleToken, nil
	}
	token, err := liftAppleMusicToken()
	if err != nil {
		return "", err
	}
	appleToken = token
	return token, nil
}

// liftAppleMusicToken pulls the JWT out of the web player's JS bundle.
func liftAppleMusicToken() (string, error) {
	client := util.NewHTTPClient(15 * time.Second)

	fetch := func(url string) (string, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", appleUserAgent)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return string(b), err
	}

	shell, err := fetch(appleBrowseURL)
	if err != nil {
		return "", fmt.Errorf("apple: fetch web player: %w", err)
	}

	for _, path := range appleBundleRe.FindAllString(shell, -1) {
		js, err := fetch("https://music.apple.com" + path)
		if err != nil {
			continue
		}
		if token := appleJWTRe.FindString(js); token != "" {
			return token, nil
		}
	}
	// Reaching here means Apple changed how the player ships its token.
	return "", fmt.Errorf("apple: no token in web player bundle (structure changed?)")
}
