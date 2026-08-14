package spotify

// Fixture-capture harness for the R2 typed rewrite. NOT a normal test: it hits
// the live Spotify GraphQL and is skipped unless SPOTIFY_CAPTURE=1, so CI and a
// plain `go test` never run it. It dumps the RAW Query() responses (the input to
// Filter*) into testdata/ so the golden tests can freeze current behavior against
// real data instead of hand-built guesses.
//
//	SPOTIFY_CAPTURE=1 go test ./backend/spotify/ -run TestCaptureFixtures -v
//
// Capturing REPLACES the golden tests' input. The goldens in testdata/ pin the
// output produced from the PREVIOUS capture, so the moment you overwrite the raw
// fixtures they no longer describe them. Always follow a capture with:
//
//	UPDATE_GOLDEN=1 go test ./backend/spotify/ -run TestFilterGolden
//
// and commit raw_*.json and golden_*.json together — they are a matched pair and
// nothing enforces that coupling for you. Skipping the second step is exactly how
// the two desync: it has already happened once, leaving three goldens failing
// against re-captured fixtures nobody had regenerated them for.
//
// The subjects below are deliberately real, and some are volatile: "Today's Top
// Hits" is re-programmed weekly and the search ranking moves, so captures months
// apart WILL legitimately change the goldens' payload — regenerate and read the
// diff. Pure capture noise (image-CDN shard rotation, live follower/listener
// counters) is absorbed by the goldens' normalizers and should produce no diff at
// all; see volatileNormalizers in filter_golden_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func dumpRaw(t *testing.T, name string, v interface{}) {
	t.Helper()
	if err := os.MkdirAll("testdata", 0755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join("testdata", name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Logf("wrote %s (%d bytes)", path, len(data))
}

func TestCaptureFixtures(t *testing.T) {
	if os.Getenv("SPOTIFY_CAPTURE") != "1" {
		t.Skip("set SPOTIFY_CAPTURE=1 to capture live fixtures")
	}

	client := NewSpotifyClient()
	if err := client.Initialize(); err != nil {
		t.Fatalf("Initialize (handshake failed — cannot reach Spotify from here?): %v", err)
	}
	t.Log("handshake OK")

	// Stable, well-known public IDs.
	const (
		trackID    = "4PTG3Z6ehGkBFwjybzWkR8" // Rick Astley — Never Gonna Give You Up
		artistID   = "0gxyHStUsqpMadRV0Di1Qt" // Rick Astley
		playlistID = "37i9dQZF1DXcBWIGoYBM5M" // Today's Top Hits
	)

	// ── track ──
	trackPayload := map[string]interface{}{
		"variables":     map[string]interface{}{"uri": fmt.Sprintf("spotify:track:%s", trackID)},
		"operationName": "getTrack",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "612585ae06ba435ad26369870deaae23b5c8800a256cd8a57e08eddc25a37294"},
		},
	}
	trackData, err := client.Query(trackPayload)
	if err != nil {
		t.Fatalf("query track: %v", err)
	}
	dumpRaw(t, "raw_track.json", trackData)

	// Discover the album ID from the track so the album fixture matches.
	albumID := ""
	if d, ok := trackData["data"].(map[string]interface{}); ok {
		if tu, ok := d["trackUnion"].(map[string]interface{}); ok {
			if aot, ok := tu["albumOfTrack"].(map[string]interface{}); ok {
				if id, ok := aot["id"].(string); ok {
					albumID = id
				}
			}
		}
	}
	if albumID != "" {
		albumPayload := map[string]interface{}{
			"variables":     map[string]interface{}{"uri": fmt.Sprintf("spotify:album:%s", albumID), "locale": "", "offset": 0, "limit": 1000},
			"operationName": "getAlbum",
			"extensions": map[string]interface{}{
				"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "b9bfabef66ed756e5e13f68a942deb60bd4125ec1f1be8cc42769dc0259b4b10"},
			},
		}
		albumData, err := client.Query(albumPayload)
		if err != nil {
			t.Fatalf("query album: %v", err)
		}
		dumpRaw(t, "raw_album.json", albumData)
	} else {
		t.Log("no album id discovered from track; skipping album fixture")
	}

	// ── playlist ──
	playlistPayload := map[string]interface{}{
		"variables": map[string]interface{}{
			"uri": fmt.Sprintf("spotify:playlist:%s", playlistID), "offset": 0, "limit": 100,
			"enableWatchFeedEntrypoint": false,
		},
		"operationName": "fetchPlaylist",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "bb67e0af06e8d6f52b531f97468ee4acd44cd0f82b988e15c2ea47b1148efc77"},
		},
	}
	if playlistData, err := client.Query(playlistPayload); err != nil {
		t.Logf("query playlist failed (non-fatal): %v", err)
	} else {
		dumpRaw(t, "raw_playlist.json", playlistData)
	}

	// ── artist overview ──
	artistPayload := map[string]interface{}{
		"variables":     map[string]interface{}{"uri": fmt.Sprintf("spotify:artist:%s", artistID), "locale": ""},
		"operationName": "queryArtistOverview",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "446130b4a0aa6522a686aafccddb0ae849165b5e0436fd802f96e0243617b5d8"},
		},
	}
	if artistData, err := client.Query(artistPayload); err != nil {
		t.Logf("query artist failed (non-fatal): %v", err)
	} else {
		dumpRaw(t, "raw_artist.json", artistData)
	}

	// ── artist discography (all) ──
	// The overview above does NOT populate artistUnion.discography.all — that's
	// a separate query merged in by fetchArtistDiscography before FilterArtist
	// runs. Capturing it lets the golden test cover FilterArtist's
	// discography/release extraction, which the overview alone leaves untested.
	discographyPayload := map[string]interface{}{
		"variables":     map[string]interface{}{"uri": fmt.Sprintf("spotify:artist:%s", artistID), "offset": 0, "limit": 50, "order": "DATE_DESC"},
		"operationName": "queryArtistDiscographyAll",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "5e07d323febb57b4a56a42abbf781490e58764aa45feb6e3dc0591564fc56599"},
		},
	}
	if discographyData, err := client.Query(discographyPayload); err != nil {
		t.Logf("query artist discography failed (non-fatal): %v", err)
	} else {
		dumpRaw(t, "raw_artist_discography.json", discographyData)
	}

	// ── search ──
	searchPayload := map[string]interface{}{
		"variables": map[string]interface{}{
			"searchTerm": "daft punk", "offset": 0, "limit": 10,
			"numberOfTopResults":            5,
			"includeAudiobooks":             true,
			"includeArtistHasConcertsField": false,
			"includePreReleases":            true,
			"includeAuthors":                false,
		},
		"operationName": "searchDesktop",
		"extensions": map[string]interface{}{
			"persistedQuery": map[string]interface{}{"version": 1, "sha256Hash": "fcad5a3e0d5af727fb76966f06971c19cfa2275e6ff7671196753e008611873c"},
		},
	}
	if searchData, err := client.Query(searchPayload); err != nil {
		t.Logf("query search failed (non-fatal): %v", err)
	} else {
		dumpRaw(t, "raw_search.json", searchData)
	}

	// ── a profile's public playlists ──
	//
	// Deliberately the `spotify` corporate account, and not only because it is
	// stable. It is the profile that misbehaves: it announces ~1500 playlists,
	// answers a 50-item request with 46, and drifts its own total between
	// calls. A personal profile answers exactly and would freeze a fixture that
	// proves nothing about the pagination rules in listProfilePlaylists.
	//
	// It must also stay a corporate account for a plainer reason: fixtures are
	// committed to a public repository, and a real person's playlist names are
	// not ours to publish.
	if body, err := client.fetchProfilePlaylistsPage(context.Background(), "spotify", 0, 50); err != nil {
		t.Logf("fetch profile playlists failed (non-fatal): %v", err)
	} else {
		var page map[string]interface{}
		if err := json.Unmarshal(body, &page); err != nil {
			t.Logf("decode profile playlists failed (non-fatal): %v", err)
		} else {
			dumpRaw(t, "raw_profile_playlists.json", page)
		}
	}
}
