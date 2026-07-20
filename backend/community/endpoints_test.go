package community

import (
	"strings"
	"testing"
)

// Pins the decrypted hosts. The byte tables above are unreadable by eye, so a
// mistyped nonce or a truncated ciphertext would otherwise fail at runtime
// against a live service rather than here.
//
// Verified live on 2026-07-20: qbz-oss and amz-oss answer /health with 200,
// verify answers 200, tdl-oss resolves (404 on /health, it has no such route).
func TestEndpointsDecryptToTheExpectedHosts(t *testing.T) {
	tests := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"tidal", TidalDownloadURL, "https://tdl-oss.spotbye.qzz.io/api/dl"},
		{"qobuz", QobuzDownloadURL, "https://qbz-oss.spotbye.qzz.io/api/dl"},
		{"qobuz health", QobuzHealthURL, "https://qbz-oss.spotbye.qzz.io/health"},
		{"amazon", AmazonDownloadURL, "https://amz-oss.spotbye.qzz.io/api/dl"},
		{"verify", VerifyBaseURL, "https://verify.spotbye.qzz.io"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("decrypt failed: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A decryption failure must be reported, not turned into a bare path. Upstream
// discards the error and returns "" + "/api/dl", so a broken table produces a
// request to a hostless URL and an error that surfaces far from its cause.
func TestDecryptFailureIsReportedNotSwallowed(t *testing.T) {
	// A tag that does not authenticate the ciphertext.
	badTag := make([]byte, len(qobuzTag))
	copy(badTag, qobuzTag)
	badTag[0] ^= 0xFF

	got, err := decryptURL(qobuzNonce, qobuzCiphertext, badTag)
	if err == nil {
		t.Fatalf("tampered tag decrypted to %q instead of failing", got)
	}
	if got != "" {
		t.Errorf("returned %q alongside the error", got)
	}
	if !strings.Contains(err.Error(), "community:") {
		t.Errorf("error %q is not attributable to this package", err)
	}
}
