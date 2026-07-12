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
	return fmt.Sprintf("Linux %s", arch), nil
}
