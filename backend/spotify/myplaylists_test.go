package spotify

import (
	"encoding/json"
	"testing"
)

func pageJSON(t *testing.T, next string, items ...map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"next": next, "items": items})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func playlist(id, ownerID string, total int) map[string]interface{} {
	return map[string]interface{}{
		"uri":  "spotify:playlist:" + id,
		"id":   id,
		"name": "Playlist " + id,
		"images": []map[string]string{
			{"url": "https://i.scdn.co/large.jpg"},
			{"url": "https://i.scdn.co/small.jpg"},
		},
		"owner":  map[string]string{"id": ownerID, "display_name": ownerID, "uri": "spotify:user:" + ownerID},
		"tracks": map[string]int{"total": total},
	}
}

// The track count is the whole reason this source exists next to the profile
// one: the profile endpoint cannot say, so the picker has no column to fill.
func TestMyPlaylistsCarriesTrackCounts(t *testing.T) {
	entries, next, err := parseMyPlaylistsPage(
		pageJSON(t, "", playlist("a", "me", 300), playlist("b", "someone", 0)), "me")
	if err != nil {
		t.Fatalf("parseMyPlaylistsPage: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q on a last page", next)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].TrackCount == nil {
		t.Fatal("no track count from the official API, which is the point of this source")
	}
	if *entries[0].TrackCount != 300 {
		t.Errorf("track count = %d, want 300", *entries[0].TrackCount)
	}
	// Zero is a real answer here, and distinguishable from "unknown" only
	// because the field is a pointer.
	if entries[1].TrackCount == nil || *entries[1].TrackCount != 0 {
		t.Errorf("an empty playlist should report 0, not unknown: %v", entries[1].TrackCount)
	}
}

// Owned separates the account's own playlists from the ones it follows, which
// is what stops someone watching a follow by accident.
func TestMyPlaylistsOwnership(t *testing.T) {
	entries, _, err := parseMyPlaylistsPage(
		pageJSON(t, "", playlist("a", "me", 1), playlist("b", "someone-else", 1)), "me")
	if err != nil {
		t.Fatalf("parseMyPlaylistsPage: %v", err)
	}
	if !entries[0].Owned {
		t.Error("the account's own playlist was not marked as owned")
	}
	if entries[1].Owned {
		t.Error("a followed playlist was marked as owned")
	}
}

// An empty selfID must under-claim rather than over-claim: everything reads as
// followed, which invites nobody to watch a follow by mistake.
func TestMyPlaylistsWithoutSelfIDClaimsNothing(t *testing.T) {
	entries, _, err := parseMyPlaylistsPage(pageJSON(t, "", playlist("a", "me", 1)), "")
	if err != nil {
		t.Fatalf("parseMyPlaylistsPage: %v", err)
	}
	if entries[0].Owned {
		t.Error("ownership was claimed with no account id to compare against")
	}
}

// Spotify orders images widest first; a list of thumbnails wants the last one.
func TestMyPlaylistsTakesTheSmallestImage(t *testing.T) {
	entries, _, err := parseMyPlaylistsPage(pageJSON(t, "", playlist("a", "me", 1)), "me")
	if err != nil {
		t.Fatalf("parseMyPlaylistsPage: %v", err)
	}
	if entries[0].ImageURL != "https://i.scdn.co/small.jpg" {
		t.Errorf("image = %q, want the smallest", entries[0].ImageURL)
	}
}

// The official API paginates honestly, with a next link that is null at the
// end — unlike the profile endpoint, whose total drifts between calls.
func TestMyPlaylistsReportsTheNextPage(t *testing.T) {
	_, next, err := parseMyPlaylistsPage(
		pageJSON(t, "https://api.spotify.com/v1/me/playlists?offset=50&limit=50",
			playlist("a", "me", 1)), "me")
	if err != nil {
		t.Fatalf("parseMyPlaylistsPage: %v", err)
	}
	if next == "" {
		t.Error("the next link was dropped; the walk would stop after one page")
	}
}

func TestListMyPlaylistsNeedsAToken(t *testing.T) {
	if _, err := ListMyPlaylists(t.Context(), "", "me"); err == nil {
		t.Error("an empty access token was accepted")
	}
}
