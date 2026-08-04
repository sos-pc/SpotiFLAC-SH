package util

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// GetFFmpegPath used to return (path, nil) when the binary was nowhere to be
// found, while GetFFprobePath returned an error for the identical situation.
// Every caller checks that error and reports "ffmpeg not found"; none of them
// ever received one, so they went on to exec a path that did not exist.
//
// The two now share an implementation, and this is the property that matters:
// not finding it is an error, for both.
func TestFFmpegBinaryErrorsWhenMissing(t *testing.T) {
	// An empty HOME with an empty PATH: nothing in the app dir, nothing to
	// look up. On Windows os.UserHomeDir reads USERPROFILE instead.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("PATH", t.TempDir())

	for _, name := range []string{"ffmpeg", "ffprobe"} {
		path, err := ffmpegBinary(name)
		if err == nil {
			t.Errorf("ffmpegBinary(%q) returned nil error for a binary that is not there (path %q)", name, path)
			continue
		}
		// The path still comes back so the API status endpoint can show where
		// the search happened.
		if path == "" {
			t.Errorf("ffmpegBinary(%q) returned an empty path alongside its error; the status endpoint displays it", name)
		}
		if !strings.Contains(err.Error(), name) {
			t.Errorf("ffmpegBinary(%q) error %q does not name the binary it looked for", name, err)
		}
	}
}

// The app directory takes precedence over PATH: it is the only way to pin a
// different build without rebuilding the image, now that nothing populates it
// automatically.
func TestFFmpegBinaryPrefersAppDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())

	appDir := filepath.Join(home, ".SpotiFLAC")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := filepath.Join(appDir, name)
	if err := os.WriteFile(want, []byte("not really a binary"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := GetFFmpegPath()
	if err != nil {
		t.Fatalf("GetFFmpegPath() = %v, want the app-dir copy", err)
	}
	if got != want {
		t.Errorf("GetFFmpegPath() = %q, want %q", got, want)
	}
}
