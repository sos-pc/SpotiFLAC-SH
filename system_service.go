package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// SystemService groups the process/config-level operations: where config
// lives, reading/writing the global settings file, server OS/defaults, and
// M3U8 file generation. Stateless — all of it derives from the config
// directory and the filesystem, nothing from the Container — so it's safe to
// construct anywhere (e.g. the watcher) with no dependencies. Extracted from
// the former App god-object (R3).
type SystemService struct{}

func (s *SystemService) GetConfigPath() (string, error) {
	dir, err := util.GetFFmpegDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

func (s *SystemService) SaveSettings(settings map[string]interface{}) error {
	configPath, err := s.GetConfigPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(configPath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write (Q7): same temp-file + rename pattern as CreateM3U8File
	// below — a crash or concurrent save mid-write can no longer leave
	// config.json truncated/corrupted on disk.
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, configPath)
}

func (s *SystemService) LoadSettings() (map[string]interface{}, error) {
	configPath, err := s.GetConfigPath()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *SystemService) GetDefaults() map[string]string {
	return map[string]string{
		"downloadPath": util.GetDefaultMusicPath(),
		// The server's OS family (runtime.GOOS: "linux"/"windows"/"darwin").
		// The frontend builds a download's output_dir for THIS server's
		// filesystem, so it must use the server's path separator + filename
		// rules, not the browser's — a Windows browser talking to a Linux
		// server would otherwise build backslash paths. See the frontend's
		// serverOSFamily() in lib/settings.ts.
		"os": runtime.GOOS,
	}
}

func (s *SystemService) GetOSInfo() (string, error) { return util.GetOSInfo() }

// CreateM3U8File génère un fichier .m3u8 de manière atomique (write-then-rename).
// musicRoot est le répertoire racine de la bibliothèque musicale locale
// (ex: "/home/nonroot/Music") utilisé pour calculer le chemin Jellyfin.
// playlistsDirName is the subfolder of the library root that every generated
// M3U8 goes into. ONE place decides this (docs/settings-source-of-truth.md D5):
// watchlist syncs and manual batch downloads used to write to different
// locations under different naming rules, so the same playlist could exist
// twice on disk with no relationship between the two files.
const playlistsDirName = "Playlists"

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
	sys := &SystemService{}
	if err := sys.CreateM3U8File(baseName, playlistDir, paths, jellyfinPath, root); err != nil {
		return result, err
	}
	result.Written = true
	return result, nil
}

func (s *SystemService) CreateM3U8File(m3u8Name string, outputDir string, filePaths []string, jellyfinMusicPath string, musicRoot string) error {
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
