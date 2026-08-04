package main

// ─────────────────────────────────────────────────────────────────────────────
// M3U8 playlist generation — shared helpers.
//
// These five lived half in watcher.go and half in system_service.go, and each
// half called into the other: the watcher counted and guarded, the service knew
// where playlists go and how to write one. Neither file could be moved without
// the other. Collected here so both sides depend on this, and this on neither.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// CreateM3U8File génère un fichier .m3u8 de manière atomique (write-then-rename).
// musicRoot est le répertoire racine de la bibliothèque musicale locale
// (ex: "/home/nonroot/Music") utilisé pour calculer le chemin Jellyfin.
// playlistsDirName is the subfolder of the library root that every generated
// M3U8 goes into. ONE place decides this (docs/settings-source-of-truth.md D5):
// watchlist syncs and manual batch downloads used to write to different
// locations under different naming rules, so the same playlist could exist
// twice on disk with no relationship between the two files.
const playlistsDirName = "Playlists"

// m3u8GenerationResult reports what generateM3U8ForPlaylist actually did,
// for callers (the repair endpoint) that need to show the user something
// more useful than fire-and-forget log lines.
type m3u8GenerationResult struct {
	Written    bool `json:"written"`    // a file was actually created/updated
	Skipped    bool `json:"skipped"`    // shrink-guard refused the write (force=false only)
	Total      int  `json:"total"`      // len(pl.TrackIDs) at generation time
	Resolved   int  `json:"resolved"`   // tracks successfully resolved to a file on disk
	Unresolved int  `json:"unresolved"` // Total - Resolved
}

// shouldSkipShrinkingWrite reports whether a new M3U8 write with newCount
// resolved entries should be skipped to avoid overwriting an existing file
// that already has more (existingCount). Only called once the caller has
// already established the shortfall is a genuine resolution gap rather than
// an intentional playlist shrink (sync_deletions removes IDs from
// pl.TrackIDs before resolution runs, so it never shows up here).
func shouldSkipShrinkingWrite(newCount, existingCount int) bool {
	return newCount < existingCount
}

// countM3U8Entries returns how many track entries the M3U8 file at path
// currently has (non-empty lines other than the #EXTM3U header), or
// ok=false if the file doesn't exist / can't be read (e.g. first-ever
// generation for this playlist — nothing to protect yet).
func countM3U8Entries(path string) (count int, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "#EXTM3U" {
			continue
		}
		count++
	}
	return count, true
}

// writeM3U8ToPlaylistsDir writes baseName.m3u8 into <root>/Playlists.
//
// total is how many tracks the caller *wanted* in the file; len(paths) is how
// many resolved to something on disk. guardShrink refuses to replace a fuller
// existing file with a shorter one — callers decide when that is a protection
// and when it would be wrong:
//
//   - a watchlist sync passes it only when tracks failed to resolve, because a
//     fully-resolved shorter list means the playlist genuinely shrank;
//   - a manual batch always passes it, because it is by nature a subset of a
//     playlist and must never clobber the complete file with a partial one.
func writeM3U8ToPlaylistsDir(
	root, baseName, jellyfinPath string, paths []string, total int, guardShrink bool,
) (m3u8GenerationResult, error) {
	result := m3u8GenerationResult{
		Total:      total,
		Resolved:   len(paths),
		Unresolved: total - len(paths),
	}
	if len(paths) == 0 {
		return result, nil
	}
	playlistDir := filepath.Join(root, playlistsDirName)
	if err := os.MkdirAll(playlistDir, 0755); err != nil {
		return result, fmt.Errorf("failed to create Playlists dir: %w", err)
	}
	if guardShrink {
		m3u8Path := filepath.Join(playlistDir, baseName+".m3u8")
		if existingCount, ok := countM3U8Entries(m3u8Path); ok &&
			shouldSkipShrinkingWrite(len(paths), existingCount) {
			slog.Warn("[M3U8] refusing to shrink, leaving the existing file untouched",
				"file", baseName+".m3u8", "existing_entries", existingCount, "new_entries", len(paths))
			result.Skipped = true
			return result, nil
		}
	}
	if err := writeM3U8File(baseName, playlistDir, paths, jellyfinPath, root); err != nil {
		return result, err
	}
	result.Written = true
	return result, nil
}

func writeM3U8File(m3u8Name string, outputDir string, filePaths []string, jellyfinMusicPath string, musicRoot string) error {
	if len(filePaths) == 0 {
		return nil
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	safeName := util.SanitizeFilename(m3u8Name)
	if safeName == "" {
		safeName = "playlist"
	}
	m3u8Path := filepath.Join(outputDir, safeName+".m3u8")
	tmpPath := m3u8Path + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	// Écriture dans le fichier temporaire — closure pour gestion d'erreur propre
	var werr error
	write := func(str string) {
		if werr != nil {
			return
		}
		_, werr = f.WriteString(str)
	}

	write("#EXTM3U\n")
	for _, path := range filePaths {
		if path == "" {
			continue
		}
		var entry string
		if jellyfinMusicPath != "" {
			// Remplacer le préfixe local (musicRoot) par le chemin Jellyfin
			localRoot := filepath.ToSlash(strings.TrimRight(musicRoot, "/"))
			entry = strings.Replace(filepath.ToSlash(path), localRoot, strings.TrimRight(jellyfinMusicPath, "/"), 1)
		} else {
			relPath, relErr := filepath.Rel(outputDir, path)
			if relErr != nil {
				relPath = path
			}
			entry = filepath.ToSlash(relPath)
		}
		write(entry + "\n")
	}

	f.Close()
	if werr != nil {
		os.Remove(tmpPath)
		return werr
	}
	// Rename atomique : jamais de fichier corrompu visible
	return os.Rename(tmpPath, m3u8Path)
}
