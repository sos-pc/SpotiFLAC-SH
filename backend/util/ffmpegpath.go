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

func GetFFmpegPath() (string, error) {
	ffmpegDir, err := GetFFmpegDir()
	if err != nil {
		return "", err
	}

	ffmpegName := "ffmpeg"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
	}

	localPath := filepath.Join(ffmpegDir, ffmpegName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	path, err := exec.LookPath(ffmpegName)
	if err == nil {
		return path, nil
	}

	return localPath, nil
}

func GetFFprobePath() (string, error) {
	ffmpegDir, err := GetFFmpegDir()
	if err != nil {
		return "", err
	}

	ffprobeName := "ffprobe"
	if runtime.GOOS == "windows" {
		ffprobeName = "ffprobe.exe"
	}

	localPath := filepath.Join(ffmpegDir, ffprobeName)
	if _, err := os.Stat(localPath); err == nil {
		return localPath, nil
	}

	path, err := exec.LookPath(ffprobeName)
	if err == nil {
		return path, nil
	}

	return localPath, fmt.Errorf("ffprobe not found in app directory or system path")
}
