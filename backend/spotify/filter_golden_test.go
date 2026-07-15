package spotify

// Characterization (golden) tests for the map-based Filter* functions. They
// freeze the CURRENT output of each Filter* so the planned typed rewrite (R2)
// can be proven behavior-preserving: rewrite, re-run, any drift fails.
//
// These tests are hermetic — they never touch Spotify. BOTH sides are frozen
// files committed to testdata/:
//
//	raw_*.json ──> Filter*() ──> output ─┐
//	                                     ├─ compared byte-for-byte
//	golden_*.json ───────────────────────┘
//
// Holding the input constant is the entire point: it isolates one variable —
// did OUR code change behavior? Spotify can reshuffle Today's Top Hits or
// rewrite its API tomorrow and these tests still pass, because they read a
// snapshot from disk, not the network (loadRawFixture is an os.ReadFile). Live
// Spotify enters only via the capture harness in capture_test.go, which is
// opt-in and never runs in CI.
//
// ── How to use these tests ─────────────────────────────────────────────────
//
// Running them: nothing special. They are offline and deterministic.
//
//	go test ./backend/spotify/ -run TestFilterGolden
//
// You changed Filter* on purpose and a golden now fails: that is the test doing
// its job. Read the diff, confirm the new output is what you intended, then
// re-pin the baseline:
//
//	UPDATE_GOLDEN=1 go test ./backend/spotify/ -run TestFilterGolden
//
// You re-captured the fixtures (SPOTIFY_CAPTURE=1): you changed the INPUT, so
// the frozen expectation no longer describes it. Regenerate the goldens in the
// same breath and commit raw_*.json and golden_*.json together — they are a
// matched pair and nothing enforces that coupling for you. Incidental capture
// noise is absorbed automatically (see volatileNormalizers below); real payload
// drift, such as a different tracklist for Today's Top Hits a week later, will
// and should fail loudly.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Values that differ between two captures of the same entity without any
// change in Filter* behavior: Spotify round-robins image URLs across CDN
// shards (image-cdn-ak / image-cdn-fa serve the byte-identical asset path),
// and follower/listener counts are live meters that tick between captures.
// Pinning them would make every re-capture (SPOTIFY_CAPTURE=1, see
// capture_test.go) fail the goldens for reasons that have nothing to do with
// the code under test.
//
// This absorbs incidental capture noise only — NOT drift in the payload
// itself. Re-capturing "Today's Top Hits" a week later changes its tracklist,
// and no normalization can (or should) hide that: the tracklist IS the output
// being characterized. Regenerate with UPDATE_GOLDEN=1 after such a capture.
//
// Trade-off: normalizing a counter to -1 means the golden no longer proves the
// count is extracted with the right VALUE, only into the right field — a
// hypothetical "followers always returns 0" bug would slip through here.
// Deliberate: these goldens exist to prove the R2 typed rewrite preserves
// behavior, and a live meter's value is environment, not behavior.
var volatileNormalizers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`image-cdn-[a-z0-9]+\.spotifycdn\.com`), "image-cdn-NORMALIZED.spotifycdn.com"},
	{regexp.MustCompile(`"(followers|listeners)": \d+`), `"${1}": -1`},
}

// normalizeVolatile rewrites capture-time-dependent values to fixed sentinels.
// Applied to both sides of every golden comparison, and to what UPDATE_GOLDEN=1
// writes, so goldens on disk are already canonical.
func normalizeVolatile(b []byte) []byte {
	for _, n := range volatileNormalizers {
		b = n.re.ReplaceAll(b, []byte(n.repl))
	}
	return b
}

func loadRawFixture(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Skipf("fixture %s missing (%v) — run the capture harness first", name, err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal fixture %s: %v", name, err)
	}
	return m
}

// assertGoldenVia mirrors the production bridge in metadata.go: every Filter*
// output is marshaled and unmarshaled into the typed apiXxxResponse it feeds
// downstream. Snapshotting THAT typed value (rather than the raw Filter output)
// makes the golden invariant to whether Filter* returns a map[string]interface{}
// (today) or builds the struct directly (after the R2 typed rewrite) — only a
// real change to a field that survives into apiXxxResponse can fail it.
func assertGoldenVia[T any](t *testing.T, golden string, filtered interface{}) {
	t.Helper()
	bridge, err := json.Marshal(filtered)
	if err != nil {
		t.Fatalf("marshal filtered: %v", err)
	}
	var typed T
	if err := json.Unmarshal(bridge, &typed); err != nil {
		t.Fatalf("unmarshal into typed contract: %v", err)
	}
	assertGolden(t, golden, typed)
}

// assertGolden marshals got canonically and compares it to testdata/<golden>.
// With UPDATE_GOLDEN=1 it (re)writes the golden instead of asserting.
func assertGolden(t *testing.T, golden string, got interface{}) {
	t.Helper()
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal got: %v", err)
	}
	gotJSON = normalizeVolatile(gotJSON)
	path := filepath.Join("testdata", golden)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, append(gotJSON, '\n'), 0644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		t.Logf("updated golden %s (%d bytes)", path, len(gotJSON))
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (%v) — generate it with UPDATE_GOLDEN=1", path, err)
	}
	// Trim the single trailing newline we append when writing.
	wantTrim := want
	if len(wantTrim) > 0 && wantTrim[len(wantTrim)-1] == '\n' {
		wantTrim = wantTrim[:len(wantTrim)-1]
	}
	// Normalized on write, but normalize on read too so a golden generated
	// before this existed still compares cleanly.
	wantTrim = normalizeVolatile(wantTrim)
	if string(gotJSON) != string(wantTrim) {
		t.Errorf("output drifted from golden %s.\n--- got ---\n%s", golden, gotJSON)
	}
}

// mergedArtistData mirrors fetchArtistDiscography (metadata.go): it merges the
// separate queryArtistDiscographyAll response into the overview's
// artistUnion.discography.all, which is what FilterArtist actually receives in
// production. Without this the overview's discography is empty and FilterArtist's
// discography/release extraction goes uncovered.
func mergedArtistData(t *testing.T) map[string]interface{} {
	overview := loadRawFixture(t, "raw_artist.json")
	disco := loadRawFixture(t, "raw_artist_discography.json")
	discoAll := getMap(getMap(getMap(disco, "data"), "artistUnion"), "discography")["all"]
	au := getMap(getMap(overview, "data"), "artistUnion")
	discography := getMap(au, "discography")
	discography["all"] = discoAll
	au["discography"] = discography
	return overview
}

// trackAlbumFetchData mirrors fetchTrack (metadata.go): FilterAlbum on the
// track's album, then reshape into the albumFetchData variadic that FilterTrack
// uses for disc-number cross-referencing. Exercises FilterTrack's albumFetch
// branch, which the bare track response leaves uncovered.
func trackAlbumFetchData(t *testing.T) map[string]interface{} {
	rawAlbum := loadRawFixture(t, "raw_album.json")
	albumJSON, err := json.Marshal(FilterAlbum(rawAlbum))
	if err != nil {
		t.Fatalf("marshal filtered album: %v", err)
	}
	var albumResp apiAlbumResponse
	if err := json.Unmarshal(albumJSON, &albumResp); err != nil {
		t.Fatalf("unmarshal apiAlbumResponse: %v", err)
	}
	respJSON, _ := json.Marshal(albumResp)
	var albumMap map[string]interface{}
	if err := json.Unmarshal(respJSON, &albumMap); err != nil {
		t.Fatalf("remarshal album map: %v", err)
	}
	tracksItems := []interface{}{}
	if tl, ok := albumMap["tracks"].([]interface{}); ok {
		for _, tr := range tl {
			tm, ok := tr.(map[string]interface{})
			if !ok {
				continue
			}
			tracksItems = append(tracksItems, map[string]interface{}{
				"track": map[string]interface{}{
					"discNumber": tm["disc_number"],
					"id":         tm["id"],
					"uri":        fmt.Sprintf("spotify:track:%s", tm["id"]),
				},
			})
		}
	}
	return map[string]interface{}{
		"data": map[string]interface{}{
			"albumUnion": map[string]interface{}{
				"discs":   map[string]interface{}{"totalCount": albumResp.Discs.TotalCount},
				"tracks":  map[string]interface{}{"items": tracksItems, "totalCount": albumResp.Count},
				"artists": albumResp.Artists,
				"label":   albumResp.Label,
			},
		},
	}
}

func TestFilterGoldenTrack(t *testing.T) {
	raw := loadRawFixture(t, "raw_track.json")
	assertGoldenVia[apiTrackResponse](t, "golden_track.json", FilterTrack(raw))
}

func TestFilterGoldenTrackWithAlbum(t *testing.T) {
	raw := loadRawFixture(t, "raw_track.json")
	assertGoldenVia[apiTrackResponse](t, "golden_track_albumfetch.json", FilterTrack(raw, trackAlbumFetchData(t)))
}

func TestFilterGoldenAlbum(t *testing.T) {
	raw := loadRawFixture(t, "raw_album.json")
	assertGoldenVia[apiAlbumResponse](t, "golden_album.json", FilterAlbum(raw))
}

func TestFilterGoldenPlaylist(t *testing.T) {
	raw := loadRawFixture(t, "raw_playlist.json")
	assertGoldenVia[apiPlaylistResponse](t, "golden_playlist.json", FilterPlaylist(raw))
}

func TestFilterGoldenArtist(t *testing.T) {
	assertGoldenVia[apiArtistResponse](t, "golden_artist.json", FilterArtist(mergedArtistData(t)))
}

func TestFilterGoldenSearch(t *testing.T) {
	raw := loadRawFixture(t, "raw_search.json")
	assertGoldenVia[apiSearchResponse](t, "golden_search.json", FilterSearch(raw))
}
