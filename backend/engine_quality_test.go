package backend

import (
	"strings"
	"testing"
)

// upstreamNormalize is a faithful port of normalize_quality() from the engine's
// SpotiFLAC/core/quality.py, read at 2026-07-28. It is here for exactly one
// assertion: that engineQualityFor never changes what a download resolves to.
//
// Sending canonical names instead of our own dialects is a refactor, not a
// quality change — but "it normalizes to the same thing" is a claim about code
// in another repository, in another language, and the only honest way to make it
// is to run both. If upstream ever moves, this port is what goes stale, and the
// comparison below is what says so.
func upstreamNormalize(q string) string {
	canonical := []struct {
		canon   string
		aliases []string
	}{
		{"HI_RES_LOSSLESS", []string{"27", "HI_RES_LOSSLESS", "HI-RES-LOSSLESS", "HIRES_LOSSLESS"}},
		{"HI_RES", []string{"7", "HI_RES", "HIRES", "HI-RES"}},
		{"LOSSLESS", []string{"6", "LOSSLESS"}},
		{"HIGH", []string{"5", "HIGH"}},
		{"LOW", []string{"4", "LOW"}},
		{"DOLBY_ATMOS", []string{"DOLBY_ATMOS", "ATMOS", "DOLBY", "EAC3", "EC3", "EAC3_JOC"}},
	}

	if q == "" {
		return "LOSSLESS"
	}
	s := strings.ToUpper(strings.TrimSpace(q))
	for _, c := range canonical {
		if s == c.canon {
			return c.canon
		}
		for _, a := range c.aliases {
			if s == a {
				return c.canon
			}
		}
	}
	if isAllDigits(s) {
		switch s {
		case "27":
			return "HI_RES_LOSSLESS"
		case "7":
			return "HI_RES"
		case "6":
			return "LOSSLESS"
		}
	}
	switch {
	case strings.Contains(s, "HI"), strings.Contains(s, "24"), strings.Contains(s, "96"):
		return "HI_RES"
	case strings.Contains(s, "LOSS"):
		return "LOSSLESS"
	case strings.Contains(s, "LOW"), strings.Contains(s, "MP3"):
		return "LOW"
	}
	return "LOSSLESS"
}

// isAllDigits mirrors Python's str.isdigit() closely enough for this table.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Every value resolveAudioFormat can produce, plus the dialects that reach
// AudioFormat from the settings UI.
var qualityInputs = []string{
	"LOSSLESS", "HI_RES_LOSSLESS", "HI_RES", // Tidal / auto
	"27", "7", "6", "5", "4", // Qobuz format IDs
	"flac",     // Deezer — the literal resolveAudioFormat returns
	"",         // unset
	" 27 ",     // padded, as a settings round-trip can leave it
	"lossless", // case
	"hi_res_lossless",
}

// The property that matters: translating to canonical names must not change what
// the engine ends up doing. If these ever disagree, some track silently changes
// bit depth — the kind of regression nobody notices until they inspect a file.
func TestEngineQualityForPreservesUpstreamNormalization(t *testing.T) {
	for _, in := range qualityInputs {
		before := upstreamNormalize(in)
		after := upstreamNormalize(engineQualityFor(in))
		if before != after {
			t.Errorf("engineQualityFor(%q) = %q: engine would resolve %q before, %q after",
				in, engineQualityFor(in), before, after)
		}
	}
}

// The point of the translation: what we put on the wire is a canonical name, so
// upstream's alias table stops being something we depend on.
func TestEngineQualityForEmitsOnlyCanonicalNames(t *testing.T) {
	canonical := map[string]bool{
		"HI_RES_LOSSLESS": true, "HI_RES": true, "LOSSLESS": true,
		"HIGH": true, "LOW": true, "DOLBY_ATMOS": true,
	}
	for _, in := range qualityInputs {
		got := engineQualityFor(in)
		if !canonical[got] {
			t.Errorf("engineQualityFor(%q) = %q, which is not a canonical name", in, got)
		}
		// Canonical in, canonical out — the function must be idempotent, or a
		// second pass anywhere in the call path would shift the value.
		if again := engineQualityFor(got); again != got {
			t.Errorf("engineQualityFor(%q) = %q, but engineQualityFor(%q) = %q — not idempotent",
				in, got, got, again)
		}
	}
}

// "flac" was the one genuinely accidental case: it matches no alias and no
// heuristic upstream, and only lands on LOSSLESS by reaching the end of the
// function. Pin the intended answer here so it stops depending on that.
func TestEngineQualityForDeezerFlacBecomesLossless(t *testing.T) {
	if got := engineQualityFor("flac"); got != "LOSSLESS" {
		t.Errorf("engineQualityFor(%q) = %q, want LOSSLESS", "flac", got)
	}
}

// The hi-res retry keys off the translated value now, so the two must agree on
// what counts as better-than-CD.
func TestIsHiResRequestAgreesWithTranslatedQuality(t *testing.T) {
	hiRes := map[string]bool{"HI_RES_LOSSLESS": true, "HI_RES": true}
	for _, in := range qualityInputs {
		q := engineQualityFor(in)
		if got, want := isHiResRequest(q), hiRes[q]; got != want {
			t.Errorf("isHiResRequest(engineQualityFor(%q)=%q) = %v, want %v", in, q, got, want)
		}
	}
}
