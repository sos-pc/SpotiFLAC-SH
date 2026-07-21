package amazon

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

func remuxWithFFmpeg(inputPath, outputPath, targetExt string) error {
	ffmpeg, err := util.GetFFmpegPath()
	if err != nil {
		return fmt.Errorf("remux: ffmpeg not found: %w", err)
	}
	if err := util.ValidateExecutable(ffmpeg); err != nil {
		return fmt.Errorf("remux: ffmpeg invalid: %w", err)
	}

	run := func(args ...string) (string, error) {
		cmd := exec.Command(ffmpeg, args...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	args := []string{"-y", "-i", inputPath, "-map", "0:a:0", "-vn", "-c:a", "copy"}
	if targetExt == ".m4a" {
		args = append(args, "-f", "mp4")
	}
	args = append(args, outputPath)

	if out, err := run(args...); err != nil {
		if targetExt == ".flac" {
			if out2, err2 := run("-y", "-i", inputPath, "-map", "0:a:0", "-vn",
				"-c:a", "flac", outputPath); err2 != nil {
				return tailError("ffmpeg remux", err2, out2)
			}
			return nil
		}
		return tailError("ffmpeg remux", err, out)
	}
	return nil
}

func normalizeAmazonQuality(quality string) string {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "16", "lossless", "cd":
		return "16"
	case "atmos", "eac3", "dolby":
		return "atmos"
	default:
		return "24"
	}
}

func targetExtForCodec(codec, quality string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	if normalizeAmazonQuality(quality) == "atmos" ||
		codec == "eac3" || codec == "ec-3" || codec == "ac-3" {
		return ".m4a"
	}
	return ".flac"
}

func tailError(context string, err error, output string) error {
	if len(output) > 500 {
		output = output[len(output)-500:]
	}
	return fmt.Errorf("%s: %w\n%s", context, err, output)
}
