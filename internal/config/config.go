package config

// ─────────────────────────────────────────────────────────────────────────────
// Settings file, read from util.AppDir().
//
// This file briefly also held getConfigDir(), extracted from main.go so auth.go
// could reach it. That turned out to be a byte-for-byte duplicate of
// util.GetFFmpegDir — the same ~/.SpotiFLAC join, written once in package main
// and once in util, because util cannot import the package holding func main().
// Both are gone: util.AppDir is the single definition, and its name now says
// what it returns.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
)

// settingsFilePath is where the user's settings blob lives on disk.
func SettingsFilePath() (string, error) {
	dir, err := util.AppDir()
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
// SaveSettingsFile writes the instance settings blob, atomically.
//
// It exists so the promotion migration in internal/settings can write without
// reaching into internal/service (which imports internal/settings, so the
// dependency would run backwards). SystemService.SaveSettings delegates here
// rather than keeping its own copy of the same temp-file-and-rename.
func SaveSettingsFile(settings map[string]interface{}) error {
	configPath, err := SettingsFilePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	// Temp file then rename: a crash or a concurrent save mid-write must not
	// leave config.json truncated, because a truncated instance store now
	// means every setting in it falls back to a default.
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

func LoadSettingsFile() (map[string]interface{}, error) {
	configPath, err := SettingsFilePath()
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
