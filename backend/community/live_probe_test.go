package community

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// Live probe: does OUR signing implementation satisfy the real service?
//
// This is the question the Python reproduction left open. It answered 401
// "Signed request validation failed" from one IP and 503 from another, and the
// server does not distinguish "bad signature" from "wrong IP" — so we could not
// tell whether the algorithm was wrong or the IP was enforced. This runs the Go
// implementation, which fixes the one bug the Python version is suspected of:
// Go's ".000" is a millisecond directive, and the script sent it literally.
//
// Reading the outcome:
//
//	400 / 404  -> the signature was ACCEPTED (only the track id is bad, which is
//	              deliberate — nothing is downloaded). If run from an IP other
//	              than the one that solved the challenge, this also proves the
//	              IP is NOT enforced.
//	401 / 428  -> rejected. From the challenge's own IP, that means our
//	              signature is still wrong. From another IP, it means the IP is
//	              enforced.
//	503        -> service cooldown, tells us nothing. Retry later.
//
// Credentials come from the environment, never from the repository: the session
// secret is a live credential for ~6 hours.
//
//	COMMUNITY_SESSION_ID=... COMMUNITY_SESSION_SECRET=... \
//	  go test ./backend/community/ -run TestLiveSignature -v
func TestLiveSignatureAgainstService(t *testing.T) {
	sessionID := os.Getenv("COMMUNITY_SESSION_ID")
	secret := os.Getenv("COMMUNITY_SESSION_SECRET")
	if sessionID == "" || secret == "" {
		t.Skip("set COMMUNITY_SESSION_ID and COMMUNITY_SESSION_SECRET to run this live probe")
	}

	appVersion := os.Getenv("COMMUNITY_APP_VERSION")
	if appVersion == "" {
		appVersion = "7.2.0"
	}

	url, err := QobuzDownloadURL()
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}

	// A deliberately invalid track id: we are testing authentication, and this
	// must not fetch anyone's music.
	body := []byte(`{"id":"0","quality":"16"}`)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	signer := Signer{SessionID: sessionID, SessionSecret: secret, AppVersion: appVersion}
	if err := signer.Sign(req); err != nil {
		t.Fatalf("sign: %v", err)
	}
	t.Logf("timestamp sent: %s", req.Header.Get("X-Sig-Timestamp"))

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	preview, _ := io.ReadAll(io.LimitReader(resp.Body, 400))

	t.Logf("HTTP %d — %s", resp.StatusCode, bytes.TrimSpace(preview))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusPreconditionRequired:
		t.Errorf("REJECTED: either the signature is wrong, or this IP is not the one that verified")
	case http.StatusServiceUnavailable:
		t.Skip("service is on cooldown — inconclusive, retry later")
	default:
		t.Logf("ACCEPTED: the signature satisfied the service (status %d is about the payload, not auth)", resp.StatusCode)
	}
}
