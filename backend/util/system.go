package util

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

func GetOSInfo() (string, error) {
	arch := runtime.GOARCH
	out, err := exec.Command("cat", "/etc/os-release").Output()
	if err == nil {
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				name := strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				return fmt.Sprintf("%s (%s)", name, arch), nil
			}
		}
	}
	return fmt.Sprintf("Linux %s", arch), nil
}
