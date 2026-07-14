package spotify

// Characterization (golden) tests for the map-based Filter* functions, pinned
// against the real captured Spotify responses in testdata/ (see
// capture_test.go). These freeze the CURRENT output of each Filter* so the
// planned typed rewrite (R2) can be proven behavior-preserving: rewrite, then
// re-run — any drift from the golden fails the test.
//
// Regenerate goldens after an intentional change with:
//   UPDATE_GOLDEN=1 go test ./backend/spotify/ -run TestFilterGolden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
	if string(gotJSON) != string(wantTrim) {
		t.Errorf("output drifted from golden %s.\n--- got ---\n%s", golden, gotJSON)
	}
}

func TestFilterGoldenTrack(t *testing.T) {
	raw := loadRawFixture(t, "raw_track.json")
	assertGoldenVia[apiTrackResponse](t, "golden_track.json", FilterTrack(raw))
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
	raw := loadRawFixture(t, "raw_artist.json")
	assertGoldenVia[apiArtistResponse](t, "golden_artist.json", FilterArtist(raw))
}

func TestFilterGoldenSearch(t *testing.T) {
	raw := loadRawFixture(t, "raw_search.json")
	assertGoldenVia[apiSearchResponse](t, "golden_search.json", FilterSearch(raw))
}
