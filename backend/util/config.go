package util

import (
	"os"
	"path/filepath"
)

func GetDefaultMusicPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/music"
	}
	return filepath.Join(homeDir, "Music")
}
