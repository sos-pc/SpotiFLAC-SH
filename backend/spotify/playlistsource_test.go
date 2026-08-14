package spotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// ── Parsing a real page ──────────────────────────────────────────────────────

func TestParseProfilePlaylistsPage(t *testing.T) {
	body := loadFixture(t, "raw_profile_playlists.json")

	entries, total, err := parseProfilePlaylistsPage(body, "spotify")
	if err != nil {
		t.Fatalf("parseProfilePlaylistsPage: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries parsed from a fixture that has them")
	}
	if total <= 0 {
		t.Errorf("announced total = %d, want a positive number", total)
	}

	// This fixture is the corporate profile answering a 50-item request. It
	// returns fewer than it was asked for — the property rule 3 in
	// listProfilePlaylists exists for. If a capture ever makes this equal, the
	// fixture has stopped covering the case it was chosen for.
	if len(entries) >= 50 {
		t.Errorf("fixture returned %d of 50 requested; it no longer exercises the short-page case", len(entries))
	}

	for _, e := range entries {
		if e.URI == "" || e.ID == "" || e.Name == "" {
			t.Errorf("incomplete entry: %+v", e)
		}
		if e.TrackCount != nil {
			t.Errorf("this source cannot know a track count, got %d for %q", *e.TrackCount, e.Name)
		}
	}

	// Every playlist on the `spotify` profile is owned by it.
	owned := 0
	for _, e := range entries {
		if e.Owned {
			owned++
		}
	}
	if owned != len(entries) {
		t.Errorf("owned = %d of %d on the account's own profile", owned, len(entries))
	}
}

// Ownership is what separates "mine" from "one I merely follow", and watching
// someone else's follow by accident is the mistake it prevents. Asserted
// against a foreign profileID rather than by trusting the fixture.
func TestParseProfilePlaylistsOwnership(t *testing.T) {
	body := loadFixture(t, "raw_profile_playlists.json")

	entries, _, err := parseProfilePlaylistsPage(body, "someone-else")
	if err != nil {
		t.Fatalf("parseProfilePlaylistsPage: %v", err)
	}
	for _, e := range entries {
		if e.Owned {
			t.Fatalf("%q is owned by %q but was marked as someone-else's own", e.Name, e.OwnerURI)
		}
	}
}

// ── The pagination rules ─────────────────────────────────────────────────────

// fakePages serves canned pages and records the offsets it was asked for.
type fakePages struct {
	pages     [][]string // playlist names per call
	requested []int      // offsets seen
	err       error
	failAt    int
}

func (f *fakePages) fetch(_ context.Context, _ string, offset, _ int) ([]byte, error) {
	f.requested = append(f.requested, offset)
	if f.err != nil && len(f.requested) == f.failAt {
		return nil, f.err
	}
	idx := len(f.requested) - 1
	var names []string
	if idx < len(f.pages) {
		names = f.pages[idx]
	}
	page := map[string]interface{}{
		"total_public_playlists_count": 1545,
		"public_playlists":             []map[string]string{},
	}
	items := make([]map[string]string, 0, len(names))
	for _, n := range names {
		items = append(items, map[string]string{
			"uri":       "spotify:playlist:" + n,
			"name":      n,
			"owner_uri": "spotify:user:someone",
		})
	}
	page["public_playlists"] = items
	return json.Marshal(page)
}

// The offset must advance by the page size REQUESTED, never by the number of
// items returned. Measured against the corporate profile: asking for 50 at
// offset 0 returns 46, and offset 50 returns a disjoint set — so counting
// survivors would re-read overlapping windows and skip what fell between.
func TestListProfilePlaylistsAdvancesByRequestedSize(t *testing.T) {
	f := &fakePages{pages: [][]string{
		{"a", "b", "c"}, // short page: 3 where 100 were asked for
		{"d", "e"},      // shorter still
		{},              // the real terminator
	}}

	got, err := listProfilePlaylists(context.Background(), "p", f.fetch)
	if err != nil {
		t.Fatalf("listProfilePlaylists: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("collected %d entries, want 5 — a short page was treated as the last", len(got))
	}

	want := []int{0, profilePlaylistsPageSize, 2 * profilePlaylistsPageSize}
	if len(f.requested) != len(want) {
		t.Fatalf("made %d requests %v, want %d", len(f.requested), f.requested, len(want))
	}
	for i, w := range want {
		if f.requested[i] != w {
			t.Errorf("request %d used offset %d, want %d — offsets are following the returned count, not the requested size",
				i, f.requested[i], w)
		}
	}
}

// The announced total is an estimate that drifts between calls (1545 → 1537 →
// 1535 → 1646 across four calls, measured). Nothing may derive an exit from it.
func TestListProfilePlaylistsIgnoresAnnouncedTotal(t *testing.T) {
	f := &fakePages{pages: [][]string{{"only"}, {}}}

	got, err := listProfilePlaylists(context.Background(), "p", f.fetch)
	if err != nil {
		t.Fatalf("listProfilePlaylists: %v", err)
	}
	// The fake announces 1545 on every page while serving one entry.
	if len(got) != 1 {
		t.Errorf("collected %d, want 1 — the walk is trusting total_public_playlists_count", len(got))
	}
}

func TestListProfilePlaylistsDeduplicates(t *testing.T) {
	f := &fakePages{pages: [][]string{{"x", "y"}, {"y", "z"}, {}}}

	got, err := listProfilePlaylists(context.Background(), "p", f.fetch)
	if err != nil {
		t.Fatalf("listProfilePlaylists: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("collected %d, want 3 unique", len(got))
	}
	seen := map[string]bool{}
	for _, e := range got {
		if seen[e.URI] {
			t.Errorf("duplicate %q survived", e.URI)
		}
		seen[e.URI] = true
	}
}

// A partial list is worse than an error: the caller would show a truncated
// picker with no way to know it was truncated, and ticking boxes on it builds a
// watchlist missing playlists the user believes they selected.
func TestListProfilePlaylistsFailsRatherThanTruncating(t *testing.T) {
	boom := errors.New("network went away")
	f := &fakePages{
		pages:  [][]string{{"a"}, {"b"}, {}},
		err:    boom,
		failAt: 2, // succeed once, then fail
	}

	got, err := listProfilePlaylists(context.Background(), "p", f.fetch)
	if err == nil {
		t.Fatalf("got %d entries and no error; want the error", len(got))
	}
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want it to wrap %v", err, boom)
	}
	if got != nil {
		t.Errorf("returned %d partial entries alongside the error", len(got))
	}
}

func TestListProfilePlaylistsStopsAtCeiling(t *testing.T) {
	// Never returns an empty page — the case the ceiling exists for.
	f := &fakePages{}
	for i := 0; i < profilePlaylistsMaxPages+10; i++ {
		f.pages = append(f.pages, []string{fmt.Sprintf("p%d", i)})
	}

	got, err := listProfilePlaylists(context.Background(), "p", f.fetch)
	if err != nil {
		t.Fatalf("listProfilePlaylists: %v", err)
	}
	if len(f.requested) != profilePlaylistsMaxPages {
		t.Errorf("made %d requests, want the ceiling of %d", len(f.requested), profilePlaylistsMaxPages)
	}
	if len(got) != profilePlaylistsMaxPages {
		t.Errorf("collected %d, want %d", len(got), profilePlaylistsMaxPages)
	}
}

// ── Profile search ───────────────────────────────────────────────────────────

func TestParseProfileSearch(t *testing.T) {
	body := loadFixture(t, "raw_search.json")
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	profiles, err := parseProfileSearch(data)
	if err != nil {
		t.Fatalf("parseProfileSearch: %v", err)
	}
	if len(profiles) == 0 {
		t.Fatal("no profiles parsed from a search fixture that has a users section")
	}
	for _, p := range profiles {
		if p.ID == "" {
			t.Errorf("profile with no id: %+v", p)
		}
	}

	// At least one profile in this fixture has "avatar": null. That is the
	// case worth freezing: the avatar is the only thing telling ten accounts
	// called "Marc" apart, and some accounts do not have one — so the view
	// needs a fallback and the alt text has to carry the id.
	missing := 0
	for _, p := range profiles {
		if p.ImageURL == "" {
			missing++
		}
	}
	if missing == 0 {
		t.Log("note: no avatar-less profile in this capture; the null-avatar path is untested here")
	}
}

func TestEntityIDFromURI(t *testing.T) {
	cases := map[string]string{
		"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M": "37i9dQZF1DXcBWIGoYBM5M",
		"spotify:user:methammer":                  "methammer",
		"nocolons":                                "",
		"trailing:":                               "",
		"":                                        "",
	}
	for in, want := range cases {
		if got := entityIDFromURI(in); got != want {
			t.Errorf("entityIDFromURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchProfilesRejectsEmptyQuery(t *testing.T) {
	// Guards before any network work; safe to call offline.
	if _, err := SearchProfiles(context.Background(), "   ", 10); err == nil {
		t.Error("a whitespace-only query was accepted")
	}
}

func TestListProfilePlaylistsRejectsEmptyID(t *testing.T) {
	if _, err := ListProfilePlaylists(context.Background(), ""); err == nil {
		t.Error("an empty profile id was accepted")
	}
}
