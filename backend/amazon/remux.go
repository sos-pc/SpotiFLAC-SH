package amazon

//nolint:unused // all functions called cross-file from community.go

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// remuxWithFFmpeg extracts the first audio stream from a decrypted MP4 and
// writes it to outputPath. The target extension determines the codec:
//   - .flac → copy FLAC bitstream if present, re-encode otherwise
//   - .m4a  → copy as-is into an MP4 container (used for Atmos/EAC3)
//
//nolint:unused
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

	// First attempt: copy codec
	args := []string{"-y", "-i", inputPath, "-map", "0:a:0", "-vn", "-c:a", "copy"}
	if targetExt == ".m4a" {
		args = append(args, "-f", "mp4")
	}
	args = append(args, outputPath)

	if out, err := run(args...); err != nil {
		// If target is FLAC and copy failed (e.g. codec mismatch), re-encode
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

// normalizeAmazonQuality maps our quality codes to the community API's values.
//
//nolint:unused
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

// targetExtForCodec returns the output extension based on the codec reported
// by the community API. Atmos/EAC3 stays in M4A; everything else becomes FLAC.
//
//nolint:unused
func targetExtForCodec(codec, quality string) string {
	codec = strings.ToLower(strings.TrimSpace(codec))
	if normalizeAmazonQuality(quality) == "atmos" ||
		codec == "eac3" || codec == "ec-3" || codec == "ac-3" {
		return ".m4a"
	}
	return ".flac"
}

//nolint:unused
func tailError(context string, err error, output string) error {
	if len(output) > 500 {
		output = output[len(output)-500:]
	}
	return fmt.Errorf("%s: %w\n%s", context, err, output)
}

// sanitizeFilename cleans a string for use as a filename component.
//
//nolint:unused
func sanitizeFilename(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|' {
			return '_'
		}
		return r
	}, s)
}

// SanitizeOptionalFilename returns s or an empty string if s is empty.
//
//nolint:unused
func SanitizeOptionalFilename(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return sanitizeFilename(s)
}

// ResolveOutputPathForDownload adds a numeric suffix if the file already exists.
//
//nolint:unused
func ResolveOutputPathForDownload(path string, suffix bool) (string, error) {
	if !suffix {
		return path, nil
	}
	dir := filepath.Dir(path)
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(filepath.Base(path), ext)
	for i := 1; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		if _, err := filepath.Glob(candidate); err != nil {
			return candidate, nil
		}
	}
	return path, nil
}
