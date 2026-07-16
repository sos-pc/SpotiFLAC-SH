package util

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ReadFFprobeTags runs ffprobe on filePath and returns all audio tags as a
// lowercase key → value map.  It is the single ffprobe invocation shared by
// filemanager.readMetadataWithFFprobe and meta.ExtractFullMetadataFromFile.
func ReadFFprobeTags(filePath string) (map[string]string, error) {
	ffprobePath, err := GetFFprobePath()
	if err != nil {
		return nil, err
	}

	if err := ValidateExecutable(ffprobePath); err != nil {
		return nil, fmt.Errorf("invalid ffprobe executable: %w", err)
	}

	// Hardened: filePath is any file in the user's library — including whatever
	// a download just wrote there. See FFprobeHardeningArgs.
	args := append(FFprobeHardeningArgs(),
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		filePath,
	)
	cmd := exec.Command(ffprobePath, args...)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	tags := make(map[string]string)
	for _, stream := range result.Streams {
		for k, v := range stream.Tags {
			tags[strings.ToLower(k)] = v
		}
	}
	for k, v := range result.Format.Tags {
		tags[strings.ToLower(k)] = v
	}
	return tags, nil
}
