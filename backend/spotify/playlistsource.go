package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// ─────────────────────────────────────────────────────────────────────────────
// Playlist sources
// ─────────────────────────────────────────────────────────────────────────────
//
// The picker offers two sources — someone's public profile, and a pasted URL —
// and both produce the same row. PlaylistEntry is that row.
//
// Both read Spotify with the anonymous web-player token, so neither asks the
// account holder to authenticate and neither can see a playlist its owner keeps
// private. That is the whole trade: no per-account setup, public playlists only.

// searchDesktopHash is the persisted-query id for Spotify's desktop search. It
// was written out at each of its three call sites; a fourth copy for profile
// search was one too many. Persisted-query hashes rotate upstream, and finding
// every copy at that moment is the whole problem.
const searchDesktopHash = "fcad5a3e0d5af727fb76966f06971c19cfa2275e6ff7671196753e008611873c"

const (
	profilePlaylistsURLTemplate = "https://spclient.wg.spotify.com/user-profile-view/v3/profile/%s/playlists?offset=%d&limit=%d"

	// Requested per page. The server does not honour it — see the pagination
	// comment in listProfilePlaylists.
	profilePlaylistsPageSize = 100

	// A ceiling, not an expectation: the loop's real exit is an empty page.
	// This only bounds the damage if the endpoint never returns one.
	profilePlaylistsMaxPages = 60
)

// PlaylistEntry is one row in the picker, whatever source produced it.
type PlaylistEntry struct {
	URI       string `json:"uri"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	ImageURL  string `json:"image_url,omitempty"`
	OwnerName string `json:"owner_name,omitempty"`
	OwnerURI  string `json:"owner_uri,omitempty"`

	// Owned separates "mine" from "ones I merely follow". A profile's public
	// playlists contain both, and watching someone else's follow by accident is
	// the mistake this flag exists to prevent.
	Owned bool `json:"owned"`
}

// ProfileSummary is a Spotify account as it appears in search results.
type ProfileSummary struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	ImageURL    string `json:"image_url,omitempty"`
}

// entityIDFromURI pulls the trailing id out of "spotify:playlist:abc123".
func entityIDFromURI(uri string) string {
	if i := strings.LastIndex(uri, ":"); i >= 0 && i+1 < len(uri) {
		return uri[i+1:]
	}
	return ""
}

// ─────────────────────────────────────────────────────────────────────────────
// A public profile's playlists
// ─────────────────────────────────────────────────────────────────────────────

// profilePlaylistsPage is the shape of one page of
// user-profile-view/v3/profile/{id}/playlists.
type profilePlaylistsPage struct {
	// Announced total. Deliberately unused for control flow — see
	// listProfilePlaylists. Kept because it is worth logging when it disagrees
	// with reality.
	TotalCount int `json:"total_public_playlists_count"`
	Playlists  []struct {
		URI       string `json:"uri"`
		Name      string `json:"name"`
		ImageURL  string `json:"image_url"`
		OwnerName string `json:"owner_name"`
		OwnerURI  string `json:"owner_uri"`
	} `json:"public_playlists"`
}

// parseProfilePlaylistsPage turns one raw page into rows. Pure, so the
// pagination rules below can be tested against frozen fixtures.
func parseProfilePlaylistsPage(body []byte, profileID string) ([]PlaylistEntry, int, error) {
	var page profilePlaylistsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, 0, fmt.Errorf("%w: profile playlists page: %v", SpotifyError, err)
	}

	selfURI := "spotify:user:" + profileID
	entries := make([]PlaylistEntry, 0, len(page.Playlists))
	for _, p := range page.Playlists {
		if p.URI == "" {
			continue
		}
		entries = append(entries, PlaylistEntry{
			URI:       p.URI,
			ID:        entityIDFromURI(p.URI),
			Name:      p.Name,
			ImageURL:  p.ImageURL,
			OwnerName: p.OwnerName,
			OwnerURI:  p.OwnerURI,
			Owned:     strings.EqualFold(p.OwnerURI, selfURI),
		})
	}
	return entries, page.TotalCount, nil
}

// ListProfilePlaylists returns every public playlist on a profile.
//
// profileID is the trailing segment of a profile URL — open.spotify.com/user/X
// — not a display name.
func ListProfilePlaylists(ctx context.Context, profileID string) ([]PlaylistEntry, error) {
	if profileID == "" {
		return nil, fmt.Errorf("%w: empty profile id", SpotifyError)
	}
	client := NewSpotifyClient()
	if err := client.Initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize spotify client: %w", err)
	}
	return listProfilePlaylists(ctx, profileID, client.fetchProfilePlaylistsPage)
}

// listProfilePlaylists holds the pagination rules, with the fetch injected so
// they can be tested without a network.
//
// Three rules, each of which this endpoint breaks if you assume otherwise.
// Measured 2026-08-13 against the `spotify` corporate profile, which announced
// 1545 playlists and answered a 50-item request with 46, then 38, then 36,
// while the announced total drifted 1545 → 1537 → 1535 → 1646 across four calls
// seconds apart:
//
//  1. Advance the offset by the page size REQUESTED, never by the number of
//     items returned. The server pages over its own window and filters within
//     it, so counting the survivors would re-read overlapping windows and skip
//     whatever fell between them.
//  2. Stop on an empty page, never on a total. The total is an estimate that
//     moves between calls, so deriving a page count from it either truncates
//     the list or loops past the end.
//  3. A short page is not the last page. 46 of 50 means 4 were filtered, not
//     that the profile is exhausted.
//
// A personal profile behaves exactly (57 announced, 57 returned, one page), so
// none of this is observable on the account most likely to be tested. That is
// precisely why it is written down.
func listProfilePlaylists(
	ctx context.Context,
	profileID string,
	fetch func(ctx context.Context, profileID string, offset, limit int) ([]byte, error),
) ([]PlaylistEntry, error) {
	var (
		all       []PlaylistEntry
		seen      = map[string]bool{}
		announced int
	)

	for page := 0; page < profilePlaylistsMaxPages; page++ {
		offset := page * profilePlaylistsPageSize

		body, err := fetch(ctx, profileID, offset, profilePlaylistsPageSize)
		if err != nil {
			// Partial results are worse than none here: the caller would show a
			// truncated list with no way to know it was truncated, and ticking
			// boxes on it produces a watchlist missing playlists the user
			// believes they selected.
			return nil, fmt.Errorf("profile %q playlists at offset %d: %w", profileID, offset, err)
		}

		entries, total, err := parseProfilePlaylistsPage(body, profileID)
		if err != nil {
			return nil, err
		}
		if page == 0 {
			announced = total
		}
		if len(entries) == 0 {
			break
		}

		// Deduplicated because the windowing above is the server's, not ours,
		// and a shifting total means the window can shift under us mid-walk.
		for _, e := range entries {
			if seen[e.URI] {
				continue
			}
			seen[e.URI] = true
			all = append(all, e)
		}

		if page == profilePlaylistsMaxPages-1 {
			slog.Warn("[Spotify] profile playlist walk hit its page ceiling",
				"profile", profileID, "collected", len(all), "announced", announced)
		}
	}

	if announced > 0 && len(all) != announced {
		// Not an error. The corporate profile disagrees with itself by
		// hundreds; a personal one matches exactly. Logged so a truncation bug
		// in here is distinguishable from Spotify being Spotify.
		slog.Debug("[Spotify] profile playlist count differs from announced",
			"profile", profileID, "collected", len(all), "announced", announced)
	}
	return all, nil
}

// fetchProfilePlaylistsPage GETs one page, retrying once on 401/403 with a
// fresh token — the same shape as fetchMetadata, for the same reason: the
// process-wide token cache can serve a token that expired since it was stored.
func (c *SpotifyClient) fetchProfilePlaylistsPage(ctx context.Context, profileID string, offset, limit int) ([]byte, error) {
	body, status, err := c.requestProfilePlaylists(ctx, profileID, offset, limit)
	if err == nil {
		return body, nil
	}
	if status != http.StatusUnauthorized && status != http.StatusForbidden {
		return nil, err
	}

	invalidateSpotifyTokenCache()
	c.mu.Lock()
	c.accessToken = ""
	c.mu.Unlock()

	body, _, err = c.requestProfilePlaylists(ctx, profileID, offset, limit)
	return body, err
}

func (c *SpotifyClient) requestProfilePlaylists(ctx context.Context, profileID string, offset, limit int) ([]byte, int, error) {
	// Token read/refresh under the lock, released before the round-trip — the
	// same pattern as Query() and requestMetadata, so concurrent callers
	// sharing one client do not serialize on every request.
	c.mu.Lock()
	if c.accessToken == "" {
		if err := c.getAccessToken(); err != nil {
			c.mu.Unlock()
			return nil, 0, err
		}
	}
	accessToken, clientToken := c.accessToken, c.clientToken
	c.mu.Unlock()

	url := fmt.Sprintf(profilePlaylistsURLTemplate, profileID, offset, limit)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if clientToken != "" {
		req.Header.Set("Client-Token", clientToken)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", util.ChromeUserAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("%w: profile playlists request failed: HTTP %d", SpotifyError, resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Profile search
// ─────────────────────────────────────────────────────────────────────────────

// parseProfileSearch extracts the `users` section of a searchDesktop response.
//
// Pure and taking the decoded map, because that is what Query() already
// returns and re-marshalling it just to re-parse would be theatre.
func parseProfileSearch(data map[string]interface{}) ([]ProfileSummary, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("%w: search response: %v", SpotifyError, err)
	}
	var envelope struct {
		Data struct {
			SearchV2 struct {
				Users struct {
					Items []struct {
						Data struct {
							ID          string `json:"id"`
							DisplayName string `json:"displayName"`
							Avatar      struct {
								Sources []struct {
									URL    string `json:"url"`
									Width  int    `json:"width"`
									Height int    `json:"height"`
								} `json:"sources"`
							} `json:"avatar"`
						} `json:"data"`
					} `json:"items"`
				} `json:"users"`
			} `json:"searchV2"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: search users section: %v", SpotifyError, err)
	}

	items := envelope.Data.SearchV2.Users.Items
	profiles := make([]ProfileSummary, 0, len(items))
	for _, it := range items {
		if it.Data.ID == "" {
			continue
		}
		// Smallest source: this is a list of avatars, not a gallery. The
		// endpoint offers 64px and 300px; sending the larger one costs the
		// client 20-odd full-size images to render at thumbnail scale.
		best := ""
		bestW := 0
		for _, s := range it.Data.Avatar.Sources {
			if s.URL == "" {
				continue
			}
			if best == "" || s.Width < bestW {
				best, bestW = s.URL, s.Width
			}
		}
		profiles = append(profiles, ProfileSummary{
			ID:          it.Data.ID,
			DisplayName: it.Data.DisplayName,
			ImageURL:    best,
		})
	}
	return profiles, nil
}

// SearchProfiles finds Spotify accounts by name.
//
// Note what this is for. Display names are not unique and ids are opaque — a
// search for "marc" returns ten profiles, most of them literally named "Marc",
// distinguishable only by their avatar. So this is a poor way to find YOURSELF
// (the connected account already knows who you are) and the right way to find
// SOMEONE ELSE, whose profile URL you do not have.
//
// The avatar being the sole discriminator is why ProfileSummary carries one,
// and why the view must put the id in its alt text rather than the name.
func SearchProfiles(ctx context.Context, query string, limit int) ([]ProfileSummary, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("%w: empty search query", SpotifyError)
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	client := NewSpotifyClient()
	if err := client.Initialize(); err != nil {
		return nil, fmt.Errorf("failed to initialize spotify client: %w", err)
	}

	// The same persisted query the track/album search already uses. Its
	// response carries a `users` section alongside the rest; nothing here asks
	// for a different operation, so there is no second hash to keep alive.
	payload := map[string]interface{}{
		"variables": map[string]interface{}{
			"searchTerm":                    query,
			"offset":                        0,
			"limit":                         limit,
			"numberOfTopResults":            5,
			"includeAudiobooks":             true,
			"includeArtistHasConcertsField": false,
			"includePreReleases":            true,
			"includeAuthors":                false,
		},
		"operationName": "searchDesktop",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{
				"version":    1,
				"sha256Hash": searchDesktopHash,
			},
		},
	}

	data, err := client.Query(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to query profile search: %w", err)
	}
	return parseProfileSearch(data)
}
