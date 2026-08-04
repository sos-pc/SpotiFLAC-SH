package util

import (
	"runtime"
	"strings"
	"testing"
)

// GetOSInfo hardcoded "Linux" in its fallback, so a Windows or macOS host was
// reported as "Linux amd64" on the system page. It only ever reads
// /etc/os-release, which exists on neither.
func TestOSDisplayName(t *testing.T) {
	for goos, want := range map[string]string{
		"linux":   "Linux",
		"windows": "Windows",
		"darwin":  "macOS",
		// Anything else passes through rather than being guessed at.
		"freebsd": "freebsd",
	} {
		if got := osDisplayName(goos); got != want {
			t.Errorf("osDisplayName(%q) = %q, want %q", goos, got, want)
		}
	}
}

// Whatever platform the tests run on, the answer must name that platform — the
// bug was a string that could not be wrong on the deployment target and was
// always wrong everywhere else.
func TestGetOSInfoNamesTheRunningPlatform(t *testing.T) {
	info, err := GetOSInfo()
	if err != nil {
		t.Fatalf("GetOSInfo() error: %v", err)
	}
	if !strings.Contains(info, runtime.GOARCH) {
		t.Errorf("GetOSInfo() = %q, missing the architecture %q", info, runtime.GOARCH)
	}
	// On Linux the name comes from /etc/os-release (a distro name, not "Linux"),
	// so only assert the fallback path on the platforms that take it.
	if runtime.GOOS != "linux" {
		if want := osDisplayName(runtime.GOOS); !strings.Contains(info, want) {
			t.Errorf("GetOSInfo() = %q on %s, want it to contain %q", info, runtime.GOOS, want)
		}
	}
}
