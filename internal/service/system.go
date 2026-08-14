package service

import (
	"path/filepath"
	"runtime"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	"github.com/sos-pc/SpotiFLAC-SH/internal/config"
	"github.com/sos-pc/SpotiFLAC-SH/internal/m3u8"
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

// SaveSettings delegates for the same reason LoadSettings does, and because the
// scope migration in internal/settings has to write the instance store too —
// internal/settings cannot import this package (this one imports it), so the
// atomic write moved to internal/config where both can reach it rather than
// being written twice.
func (s *SystemService) SaveSettings(settings map[string]interface{}) error {
	return config.SaveSettingsFile(settings)
}

// LoadSettings delegates to loadSettingsFile so that callers who only need the
// settings — download_settings.go did, and had to construct a throwaway
// SystemService to get at them — can call the function directly.
func (s *SystemService) LoadSettings() (map[string]interface{}, error) {
	return config.LoadSettingsFile()
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

// CreateM3U8File delegates to m3u8.WriteFile. Kept as a method because the API
// layer calls it through the service; the work itself needs no instance.
func (s *SystemService) CreateM3U8File(m3u8Name string, outputDir string, filePaths []string, jellyfinMusicPath string, musicRoot string) error {
	return m3u8.WriteFile(m3u8Name, outputDir, filePaths, jellyfinMusicPath, musicRoot)
}
