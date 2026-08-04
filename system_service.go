package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// SystemService groups the process/config-level operations: where config
// lives, reading/writing the global settings file, server OS/defaults, and
// M3U8 file generation. Stateless — all of it derives from the config
// directory and the filesystem, nothing from the Container — so it's safe to
// construct anywhere (e.g. the watcher) with no dependencies. Extracted from
// the former App god-object (R3).
type SystemService struct{}

func (s *SystemService) GetConfigPath() (string, error) {
	dir, err := util.AppDir()
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

// LoadSettings delegates to loadSettingsFile so that callers who only need the
// settings — download_settings.go did, and had to construct a throwaway
// SystemService to get at them — can call the function directly.
func (s *SystemService) LoadSettings() (map[string]interface{}, error) {
	return loadSettingsFile()
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

// CreateM3U8File delegates to writeM3U8File. Kept as a method because the API
// layer calls it through the service; the work itself needs no instance.
func (s *SystemService) CreateM3U8File(m3u8Name string, outputDir string, filePaths []string, jellyfinMusicPath string, musicRoot string) error {
	return writeM3U8File(m3u8Name, outputDir, filePaths, jellyfinMusicPath, musicRoot)
}
