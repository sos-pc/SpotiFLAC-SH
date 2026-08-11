package watcher

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/m3u8"
)

// TestM3U8BaseNameAvoidsCollisions is the regression test for two
// watchlists whose names collide after sanitization: they must produce
// different filenames, so neither silently overwrites the other's M3U8.
func TestM3U8BaseNameAvoidsCollisions(t *testing.T) {
	nameA := m3u8BaseName("AC/DC Hits", "watch-1")
	nameB := m3u8BaseName("AC:DC Hits", "watch-2")

	// Confirm the premise: these two names really do collide once
	// sanitized without the ID suffix (otherwise this test proves nothing).
	if legacyM3U8BaseName("AC/DC Hits") != legacyM3U8BaseName("AC:DC Hits") {
		t.Fatal("test premise broken: these two names no longer collide after sanitization")
	}

	if nameA == nameB {
		t.Errorf("m3u8BaseName produced the same filename for two different watchlists: %q", nameA)
	}
}

// TestM3U8BaseNameStableForSameWatchlist confirms the name doesn't churn
// across calls for the same watchlist (needed for the shrink-guard and the
// rename-cleanup logic, both of which recompute the path independently and
// must agree with what was actually written).
func TestM3U8BaseNameStableForSameWatchlist(t *testing.T) {
	a := m3u8BaseName("My Playlist", "watch-123")
	b := m3u8BaseName("My Playlist", "watch-123")
	if a != b {
		t.Errorf("m3u8BaseName is not stable: got %q then %q for identical inputs", a, b)
	}
}

// TestLegacyM3U8BaseNameMatchesOldScheme confirms the legacy (pre-migration)
// name is exactly what CreateM3U8File used to produce (sanitized name only,
// no suffix) — the migration cleanup depends on this matching precisely to
// find and remove the old file.
func TestLegacyM3U8BaseNameMatchesOldScheme(t *testing.T) {
	got := legacyM3U8BaseName("My Playlist")
	if got != "My Playlist" {
		t.Errorf("legacyM3U8BaseName(%q) = %q, want unchanged sanitized name", "My Playlist", got)
	}
}

func TestCountM3U8Entries(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		writeFile bool
		wantCount int
		wantOK    bool
	}{
		{"missing file", "", false, 0, false},
		{"header only", "#EXTM3U\n", true, 0, true},
		{"three entries", "#EXTM3U\n/music/a.flac\n/music/b.flac\n/music/c.flac\n", true, 3, true},
		{"blank lines ignored", "#EXTM3U\n\n/music/a.flac\n\n\n/music/b.flac\n", true, 2, true},
		{"empty file", "", true, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "playlist.m3u8")
			if tt.writeFile {
				if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
			}
			count, ok := m3u8.CountEntries(path)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if count != tt.wantCount {
				t.Errorf("count = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

// TestIsAlbumSource covers the URL shapes both M3U8 producers hand it — a
// watchlist's SpotifyURL and a manual batch's SourceID are the same thing.
//
// The artist case is asserted false on purpose: not covering artists is a
// decision, and a test is where that stays a decision rather than becoming an
// accident. The empty case matters too: a batch started with no source URL must
// still get its playlist rather than be silently classified as an album.
func TestIsAlbumSource(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://open.spotify.com/album/1DFixLWuPkv3KT3TnV35m3", true},
		{"https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M", false},
		{"https://open.spotify.com/artist/0OdUWJ0sBjDrqHygGUXeCF", false},
		{"spotify:album:1DFixLWuPkv3KT3TnV35m3", true},
		{"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M", false},
		{"https://open.spotify.com/intl-fr/ALBUM/1DFixLWuPkv3KT3TnV35m3", true},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isAlbumSource(tt.url); got != tt.want {
				t.Errorf("isAlbumSource(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestShouldSkipShrinkingWrite(t *testing.T) {
	tests := []struct {
		name          string
		newCount      int
		existingCount int
		want          bool
	}{
		{"new smaller than existing — skip", 5, 50, true},
		{"new equal to existing — write (no shrink)", 50, 50, false},
		{"new larger than existing — write", 60, 50, false},
		{"existing empty — write", 5, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m3u8.ShouldSkipShrinkingWrite(tt.newCount, tt.existingCount)
			if got != tt.want {
				t.Errorf("m3u8.ShouldSkipShrinkingWrite(%d, %d) = %v, want %v", tt.newCount, tt.existingCount, got, tt.want)
			}
		})
	}
}
