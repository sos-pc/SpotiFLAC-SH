package util

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

func GetOSInfo() (string, error) {
	arch := runtime.GOARCH
	data, err := os.ReadFile("/etc/os-release")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				name := strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				return fmt.Sprintf("%s (%s)", name, arch), nil
			}
		}
	}
	// /etc/os-release exists only on Linux. Reaching here means either a
	// non-Linux host or a distro without it, so report what the runtime knows
	// instead of the literal "Linux" this printed on every platform — which was
	// a leftover from when Docker was the only target considered, and made the
	// system page claim "Linux amd64" on a Windows dev machine.
	return fmt.Sprintf("%s %s", osDisplayName(runtime.GOOS), arch), nil
}

// osDisplayName turns a GOOS value into something a user recognises. "darwin"
// is the one that genuinely needs it.
func osDisplayName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	case "windows":
		return "Windows"
	default:
		return goos
	}
}
