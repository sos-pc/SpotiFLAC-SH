package main

import (
	"os"
	"path/filepath"
	"testing"
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
			count, ok := countM3U8Entries(path)
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
			got := shouldSkipShrinkingWrite(tt.newCount, tt.existingCount)
			if got != tt.want {
				t.Errorf("shouldSkipShrinkingWrite(%d, %d) = %v, want %v", tt.newCount, tt.existingCount, got, tt.want)
			}
		})
	}
}
