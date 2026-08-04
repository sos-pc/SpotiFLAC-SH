package util

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

func ValidateExecutable(path string) error {
	cleanedPath := filepath.Clean(path)
	if cleanedPath == "" {
		return fmt.Errorf("empty path")
	}

	if !filepath.IsAbs(cleanedPath) {
		return fmt.Errorf("path must be absolute: %s", path)
	}

	info, err := os.Stat(cleanedPath)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	if info.IsDir() {
		return fmt.Errorf("path is a directory: %s", path)
	}

	if runtime.GOOS != "windows" {
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("file is not executable: %s", path)
		}
	}

	base := filepath.Base(cleanedPath)
	validNames := map[string]bool{
		"ffmpeg":      true,
		"ffmpeg.exe":  true,
		"ffprobe":     true,
		"ffprobe.exe": true,
	}
	if !validNames[base] {
		return fmt.Errorf("invalid executable name: %s", base)
	}

	return nil
}

func GetFFmpegDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(homeDir, ".SpotiFLAC"), nil
}

// ffmpegBinary resolves one of the FFmpeg tools, in two places and in this
// order:
//
//  1. ~/.SpotiFLAC — where the first-launch auto-installer used to put it. That
//     installer was deleted on 2026-08-04 (it downloaded from a repository that
//     no longer exists), so nothing populates this directory any more. It is
//     kept as a manual override: a binary dropped there still wins over the
//     system one, which is the only way to pin a different build without
//     rebuilding the image.
//  2. $PATH — what the Docker image actually uses. FFmpeg is baked in at build
//     time from BtbN/FFmpeg-Builds.
//
// On failure it returns the path it looked for *and* an error: the API status
// endpoint displays that path to show where the search happened.
//
// GetFFmpegPath used to return (nonexistentPath, nil) here while GetFFprobePath
// returned an error — the same function written twice, disagreeing. Every caller
// already checks the error and reports "ffmpeg not found"; they simply never
// received one, and proceeded to exec a path that was not there.
func ffmpegBinary(name string) (string, error) {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	dir, err := GetFFmpegDir()
	if err != nil {
		return "", err
	}

	local := filepath.Join(dir, name)
	if _, err := os.Stat(local); err == nil {
		return local, nil
	}

	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}

	return local, fmt.Errorf("%s not found in %s or on PATH", name, dir)
}

func GetFFmpegPath() (string, error)  { return ffmpegBinary("ffmpeg") }
func GetFFprobePath() (string, error) { return ffmpegBinary("ffprobe") }
