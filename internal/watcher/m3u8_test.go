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

// TestNeedsFilesystemIndexFallback is the regression test for the
// resolveTrackPaths gating bug: a single stale catalog row (renamed/moved
// file) must still trigger the filesystem fallback scan, even when the
// raw catalog row count matches the playlist's track count.
func TestNeedsFilesystemIndexFallback(t *testing.T) {
	tests := []struct {
		name         string
		trackIDs     []string
		validCatalog map[string]string
		want         bool
	}{
		{
			name:         "all tracks resolved via catalog — no fallback needed",
			trackIDs:     []string{"a", "b", "c"},
			validCatalog: map[string]string{"a": "/p/a.flac", "b": "/p/b.flac", "c": "/p/c.flac"},
			want:         false,
		},
		{
			name:         "one track has no catalog row at all — needs fallback",
			trackIDs:     []string{"a", "b", "c"},
			validCatalog: map[string]string{"a": "/p/a.flac", "b": "/p/b.flac"},
			want:         true,
		},
		{
			name:     "one track's catalog row is stale (renamed file) — needs fallback even though row COUNT would look satisfied",
			trackIDs: []string{"a", "b", "c"},
			// validCatalog only holds STAT-VALID entries — a stale row for
			// "c" was already excluded by the caller before this is
			// called, so len(validCatalog) < len(trackIDs) here even
			// though a raw (unfiltered) catalog row existed for "c".
			validCatalog: map[string]string{"a": "/p/a.flac", "b": "/p/b.flac"},
			want:         true,
		},
		{
			name:         "empty playlist",
			trackIDs:     nil,
			validCatalog: map[string]string{},
			want:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsFilesystemIndexFallback(tt.trackIDs, tt.validCatalog)
			if got != tt.want {
				t.Errorf("needsFilesystemIndexFallback(%v, %v) = %v, want %v", tt.trackIDs, tt.validCatalog, got, tt.want)
			}
		})
	}
}

// TestIsAlbumWatchlist covers the URL shapes a watchlist can hold. The artist
// case is asserted false on purpose: not covering artists is a decision, and a
// test is where that stays a decision rather than becoming an accident.
func TestIsAlbumWatchlist(t *testing.T) {
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
			if got := isAlbumWatchlist(tt.url); got != tt.want {
				t.Errorf("isAlbumWatchlist(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestReconcileOnePlaylistDir is the safety net for code that deletes files.
//
// The rule it protects: only names matching m3u8BaseName's "<name> [8 hex]"
// shape may ever be removed. The Playlists directory is ours by convention, not
// by ownership, so an operator's own playlist sitting there must survive a
// reconcile — losing someone's file while tidying up our own orphan would be a
// far worse bug than the orphan.
func TestReconcileOnePlaylistDir(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"Release Radar [830f8305].m3u8", // kept: a live watchlist owns it
		"all [957f2ab0].m3u8",           // removed: suffixed, unowned
		"Free Ride [fc8b49be].m3u8",     // removed: suffixed, unowned (album)
		"My Own Mix.m3u8",               // kept: not suffixed — operator's file
		"Legacy Playlist.m3u8",          // kept: not suffixed
		"weird [xyz12345].m3u8",         // kept: bracketed but not 8 hex digits
		"notes.txt",                     // kept: not an m3u8
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", f, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "a directory [deadbeef].m3u8"), 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	keep := map[string]struct{}{"Release Radar [830f8305].m3u8": {}}
	(&Watcher{}).reconcileOnePlaylistDir(dir, keep)

	wantGone := []string{"all [957f2ab0].m3u8", "Free Ride [fc8b49be].m3u8"}
	wantKept := []string{
		"Release Radar [830f8305].m3u8",
		"My Own Mix.m3u8",
		"Legacy Playlist.m3u8",
		"weird [xyz12345].m3u8",
		"notes.txt",
		"a directory [deadbeef].m3u8",
	}
	for _, f := range wantGone {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%q should have been removed as an orphan", f)
		}
	}
	for _, f := range wantKept {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%q should have been left alone, got %v", f, err)
		}
	}
}

// TestReconcileOnePlaylistDirEmptyKeepSet covers the all-albums and
// last-watchlist-deleted cases: an empty keep set means every suffixed file is
// unowned, and every one of them must go — while everything else still stays.
func TestReconcileOnePlaylistDirEmptyKeepSet(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a [11111111].m3u8", "b [22222222].m3u8", "mine.m3u8"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("#EXTM3U\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q): %v", f, err)
		}
	}

	(&Watcher{}).reconcileOnePlaylistDir(dir, map[string]struct{}{})

	for _, f := range []string{"a [11111111].m3u8", "b [22222222].m3u8"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%q should have been removed with an empty keep set", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "mine.m3u8")); err != nil {
		t.Errorf("unsuffixed file must survive an empty keep set, got %v", err)
	}
}

// TestReconcileMissingDirIsNotAnError: a deployment that has never generated a
// playlist has no Playlists directory, and reconcile runs on every check cycle.
func TestReconcileMissingDirIsNotAnError(t *testing.T) {
	(&Watcher{}).reconcileOnePlaylistDir(filepath.Join(t.TempDir(), "nope"), map[string]struct{}{})
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
