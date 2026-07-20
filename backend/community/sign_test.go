package community

import (
	"bytes"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func testSigner() Signer {
	return Signer{
		SessionID:     "session-abc",
		SessionSecret: "secret-xyz",
		AppVersion:    "7.2.0",
	}
}

func TestSignSetsEverySignatureHeader(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://qbz-oss.spotbye.qzz.io/api/dl",
		bytes.NewReader([]byte(`{"id":"1"}`)))
	if err := testSigner().Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	for _, h := range []string{
		"X-Sig-Session", "X-Sig-Timestamp", "X-Sig-Nonce",
		"X-Sig-Body-SHA256", "X-Sig-Signature", "X-Sig-App-Version", "X-Sig-Platform",
	} {
		if req.Header.Get(h) == "" {
			t.Errorf("%s is empty", h)
		}
	}
	if got := req.Header.Get("X-Sig-Platform"); got != Platform {
		t.Errorf("platform = %q, want %q", got, Platform)
	}
}

// The body must still be readable afterwards: signing consumes it, and a
// request whose body was drained sends Content-Length bytes of nothing.
func TestSignLeavesTheBodyReadable(t *testing.T) {
	payload := `{"id":"8767428","quality":"6"}`
	req, _ := http.NewRequest(http.MethodPost, "https://x/api/dl", strings.NewReader(payload))
	if err := testSigner().Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != payload {
		t.Errorf("body = %q, wanted it intact", got)
	}
}

// The hash covers the exact bytes sent. A mismatch is one of the two ways the
// server answers "Signed request validation failed" without saying which.
func TestBodyHashMatchesTheBodySent(t *testing.T) {
	payload := []byte(`{"id":"1"}`)
	req, _ := http.NewRequest(http.MethodPost, "https://x/api/dl", bytes.NewReader(payload))
	if err := testSigner().Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got := req.Header.Get("X-Sig-Body-SHA256")
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("hash is not hex: %q", got)
	}
	// An empty body must hash to the digest of nothing, not be skipped.
	req2, _ := http.NewRequest(http.MethodGet, "https://x/health", nil)
	if err := testSigner().Sign(req2); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := req2.Header.Get("X-Sig-Body-SHA256"); got != emptySHA256 {
		t.Errorf("empty-body hash = %q, want %q", got, emptySHA256)
	}
}

// The timestamp must carry real milliseconds. Go's ".000" is a format
// directive, and my first reproduction of this algorithm sent a literal ".000"
// — a plausible source of the 401 we could not explain.
func TestTimestampCarriesRealMilliseconds(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://x/health", nil)
		if err := testSigner().Sign(req); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		ts := req.Header.Get("X-Sig-Timestamp")
		if !strings.HasSuffix(ts, "Z") || len(ts) != len("2006-01-02T15:04:05.000Z") {
			t.Fatalf("timestamp %q does not match the expected layout", ts)
		}
		seen[ts[strings.LastIndex(ts, ".")+1:len(ts)-1]] = true
		time.Sleep(time.Millisecond)
	}
	if len(seen) == 1 {
		t.Error("every timestamp had the same millisecond field — the layout is being sent literally")
	}
}

// Two signatures of the same request must differ: the nonce is fresh each time.
// Identical signatures would make a captured request replayable within its window.
func TestSignatureIsNotReplayable(t *testing.T) {
	sign := func() (string, string) {
		req, _ := http.NewRequest(http.MethodPost, "https://x/api/dl", strings.NewReader(`{"id":"1"}`))
		if err := testSigner().Sign(req); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return req.Header.Get("X-Sig-Signature"), req.Header.Get("X-Sig-Nonce")
	}
	sig1, nonce1 := sign()
	sig2, nonce2 := sign()
	if nonce1 == nonce2 {
		t.Error("nonce was reused")
	}
	if sig1 == sig2 {
		t.Error("signature was identical across calls")
	}
}

// The rolling key changes at each window boundary and is stable within one —
// this is what lets a multi-hour download keep signing without any interaction.
func TestRollingKeyRotatesPerWindow(t *testing.T) {
	s := testSigner()
	base := time.Unix(1784547600, 0).UTC() // aligned on a 300s boundary

	sameWindow := []time.Time{base, base.Add(time.Second), base.Add(299 * time.Second)}
	first := s.rollingKey(sameWindow[0])
	for _, ts := range sameWindow[1:] {
		if !bytes.Equal(first, s.rollingKey(ts)) {
			t.Errorf("key changed inside one window at %v", ts)
		}
	}
	if bytes.Equal(first, s.rollingKey(base.Add(300*time.Second))) {
		t.Error("key did not change at the window boundary")
	}
	// A different session must not derive the same key from the same window.
	other := Signer{SessionID: "other", SessionSecret: s.SessionSecret, AppVersion: s.AppVersion}
	if bytes.Equal(first, other.rollingKey(base)) {
		t.Error("two sessions derived the same key")
	}
}

func TestSignRefusesWithoutASession(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://x/health", nil)
	if err := (Signer{AppVersion: "7.2.0"}).Sign(req); err == nil {
		t.Error("signed with no session id or secret")
	}
}
