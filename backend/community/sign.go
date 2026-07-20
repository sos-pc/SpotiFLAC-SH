package community

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// signatureVersion is the literal that opens every signing payload. The server
// rejects anything else, so it doubles as a protocol version marker.
const signatureVersion = "SPOTIFLAC-HMAC-V1"

// rollingWindowSeconds is how long one derived signing key stays usable.
//
// This is the answer to "how can a 6-hour download survive a 5-minute window":
// the key rotates on its own, computed locally from the session secret, with no
// interaction of any kind. Only the *session* expiry needs a human.
const rollingWindowSeconds = 300

// timestampLayout is Go's reference time in the format the server expects.
// Note the .000 — it is a format directive for milliseconds, NOT a literal, so
// this emits the real millisecond field. Sending a hard-coded ".000" produces a
// timestamp the server may hash differently than we do.
const timestampLayout = "2006-01-02T15:04:05.000Z"

// Platform is what we declare ourselves as, in the bootstrap, the grant
// exchange and every signature — the three must agree or the server refuses.
//
// It is "desktop" because that is the only value observed to be accepted, and
// because the value is sealed into the signed challenge token at bootstrap:
// changing it here alone would produce a session whose token says one thing and
// whose requests say another. Declaring a self-hosted server as a desktop
// client is a compromise, recorded here rather than buried.
const Platform = "desktop"

// Signer holds the credentials needed to sign requests for one session.
type Signer struct {
	SessionID     string
	SessionSecret string
	AppVersion    string
}

// rollingKey derives the signing key for the window containing t.
//
// Two levels of HMAC: the session secret signs the window marker, and the
// result signs the request. A key captured from one request is therefore only
// useful for the remainder of its 5-minute window.
func (s Signer) rollingKey(t time.Time) []byte {
	input := fmt.Sprintf("%d:%s", t.Unix()/rollingWindowSeconds, s.SessionID)
	return hmacSHA256([]byte(s.SessionSecret), []byte(input))
}

// Sign attaches the X-Sig-* headers to req, reading and restoring its body.
//
// The signed payload is ten newline-separated fields, and the empty fourth one
// is load-bearing: it is the query string, empty for these endpoints. Dropping
// it shifts every following field and produces a signature the server rejects
// with a message that names none of this.
func (s Signer) Sign(req *http.Request) error {
	if s.SessionID == "" || s.SessionSecret == "" {
		return fmt.Errorf("community: cannot sign without a session")
	}

	body := []byte{}
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("community: read body for signing: %w", err)
		}
		req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	sum := sha256.Sum256(body)
	bodyHash := hex.EncodeToString(sum[:])

	now := time.Now().UTC()
	timestamp := now.Format(timestampLayout)
	nonce, err := randomHex(12)
	if err != nil {
		return err
	}

	// The window is taken from the same instant that produced the timestamp,
	// not from a second call to the clock: a request crossing a window boundary
	// between the two would sign for one window and be verified against another.
	signingInput := strings.Join([]string{
		signatureVersion,
		req.Method,
		req.URL.EscapedPath(),
		"", // query string
		bodyHash,
		timestamp,
		nonce,
		s.SessionID,
		s.AppVersion,
		Platform,
	}, "\n")

	signature := base64.RawURLEncoding.EncodeToString(
		hmacSHA256(s.rollingKey(now), []byte(signingInput)))

	req.Header.Set("X-Sig-Session", s.SessionID)
	req.Header.Set("X-Sig-Timestamp", timestamp)
	req.Header.Set("X-Sig-Nonce", nonce)
	req.Header.Set("X-Sig-Body-SHA256", bodyHash)
	req.Header.Set("X-Sig-Signature", signature)
	req.Header.Set("X-Sig-App-Version", s.AppVersion)
	req.Header.Set("X-Sig-Platform", Platform)
	return nil
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("community: entropy unavailable: %w", err)
	}
	return hex.EncodeToString(b), nil
}
