package main

// ─────────────────────────────────────────────────────────────────────────────
// Config paths.
//
// Extracted from main.go: auth.go needs getConfigDir too, and a package cannot
// import the one holding func main(). Config resolution is more fundamental
// than either of them anyway — it is what they are both configured by.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// getConfigDir retourne le dossier de config SpotiFLAC.
// Sous Docker : /home/nonroot/.SpotiFLAC
// En local    : ~/.SpotiFLAC
func getConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".SpotiFLAC"), nil
}

// settingsFilePath is where the user's settings blob lives on disk.
func settingsFilePath() (string, error) {
	dir, err := util.GetFFmpegDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// loadSettingsFile reads that blob. A missing file is not an error: it is a
// fresh install, and every caller treats a nil map as "use the defaults".
//
// A plain function rather than a method: it reads a file and parses JSON, and
// nothing about that needs a service instance. Making it one meant
// download_settings.go had to write `(&SystemService{}).LoadSettings()` — a
// throwaway object built purely to reach a method, and a dependency from the
// settings domain onto the service layer for no behaviour at all.
func loadSettingsFile() (map[string]interface{}, error) {
	configPath, err := settingsFilePath()
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
