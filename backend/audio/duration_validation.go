package audio

import (
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// Thresholds for ValidateTrackDuration. Ported from upstream's
// download_validation.go (docs/upstream-catchup.md §S2) and kept as-is: measured
// against 13 real library files on 2026-07-18, 12 of them landed within 2% of
// the Spotify duration, the worst was 13%, none exceeded 25%.
const (
	// A community Tidal proxy without a Premium token serves a ~30s sample
	// instead of the track (assetPresentation: PREVIEW). That is the failure
	// this exists for: it is silent, and it fills a library with junk that
	// looks fine in a file listing.
	previewMaxSeconds         = 35
	previewExpectedMinSeconds = 60

	// The generic mismatch rule is deliberately much looser, and only applies
	// to tracks long enough for a percentage to mean anything. Legitimate
	// differences are common — remasters, radio edits, a provider's metadata
	// simply disagreeing with Spotify's — and deleting a good file is worse
	// than keeping a questionable one.
	largeMismatchMinExpected = 90
	minAllowedDurationDiff   = 15
	durationDiffRatio        = 0.25
)

// ProbeDuration returns a media file's duration in seconds.
//
// Deliberately not AnalyzeTrack: that one computes a full spectrum, which is
// far too heavy to run after every single download.
func ProbeDuration(filePath string) (float64, error) {
	ffprobePath, err := util.GetFFprobePath()
	if err != nil {
		return 0, err
	}
	// Hardened the same way AnalyzeTrack is: filePath is a file a third-party
	// provider chose the contents of.
	args := append(util.FFprobeHardeningArgs(),
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		filePath,
	)
	cmd := exec.Command(ffprobePath, args...)
	setHideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe failed: %w - %s", err, string(output))
	}
	d, err := strconv.ParseFloat(strings.TrimSpace(string(output)), 64)
	if err != nil {
		return 0, fmt.Errorf("could not parse duration %q: %w", strings.TrimSpace(string(output)), err)
	}
	return d, nil
}

// ValidateTrackDuration reports whether a downloaded file plausibly contains the
// track that was asked for, by comparing its real duration to the one Spotify
// advertises.
//
// It returns nil when the file is acceptable OR when no judgement can be made
// (no expected duration, unreadable file). Refusing to judge is deliberate: a
// probe failure must not delete a file that may well be fine.
func ValidateTrackDuration(filePath string, expectedSeconds int) error {
	if filePath == "" || expectedSeconds <= 0 {
		return nil
	}
	actual, err := ProbeDuration(filePath)
	if err != nil || actual <= 0 {
		return nil
	}
	return validateDurations(int(math.Round(actual)), expectedSeconds)
}

// validateDurations holds the decision itself, with no I/O, so the thresholds
// can be tested without producing audio files.
func validateDurations(actualSeconds, expectedSeconds int) error {
	if actualSeconds <= 0 || expectedSeconds <= 0 {
		return nil
	}

	if expectedSeconds >= previewExpectedMinSeconds && actualSeconds <= previewMaxSeconds {
		return fmt.Errorf("preview detected: file is %ds, expected about %ds", actualSeconds, expectedSeconds)
	}

	if expectedSeconds >= largeMismatchMinExpected {
		allowed := int(math.Max(minAllowedDurationDiff, math.Round(float64(expectedSeconds)*durationDiffRatio)))
		if diff := int(math.Abs(float64(actualSeconds - expectedSeconds))); diff > allowed {
			return fmt.Errorf("duration mismatch: file is %ds, expected about %ds (tolerance %ds)",
				actualSeconds, expectedSeconds, allowed)
		}
	}

	return nil
}
