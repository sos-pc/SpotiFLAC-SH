package qobuz

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The signature is the whole point: an unsigned request is what returned 401.
// This recomputes it independently from the URL the code produced and checks
// they agree, so a change to the concatenation order, the slash-stripping, or
// the excluded params fails here rather than as a live 401 nobody can explain.
func TestSignedURLMatchesAnIndependentSignature(t *testing.T) {
	raw := signedQobuzURL("track/search", map[string]string{
		"query": "GBBPW0600149",
		"limit": "1",
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("produced an unparseable URL: %v", err)
	}
	q := u.Query()

	if got := q.Get("app_id"); got != signedAppID {
		t.Errorf("app_id = %q, want %q", got, signedAppID)
	}
	if q.Get("request_ts") == "" || q.Get("request_sig") == "" {
		t.Fatal("request_ts or request_sig missing")
	}

	// Recompute the signature the way the web player does, from the params that
	// actually went out, and confirm the code signed the same thing.
	ts := q.Get("request_ts")
	// Sorted "key"+"value" over everything except the signature envelope.
	want := "tracksearch" + "limit" + "1" + "query" + "GBBPW0600149" + ts + signedSecret
	sum := md5.Sum([]byte(want))
	expected := hex.EncodeToString(sum[:])

	if got := q.Get("request_sig"); got != expected {
		t.Errorf("signature = %q, want %q\n(signed payload = %q)", got, expected, want)
	}
}

// The three envelope params must never be folded into their own signature.
func TestSignatureExcludesItsOwnEnvelope(t *testing.T) {
	raw := signedQobuzURL("track/search", map[string]string{"query": "x"})
	q, _ := url.ParseQuery(strings.SplitN(raw, "?", 2)[1])

	ts := q.Get("request_ts")
	// Only "query" is a real param here; app_id/request_ts/request_sig excluded.
	want := "tracksearch" + "query" + "x" + ts + signedSecret
	sum := md5.Sum([]byte(want))
	if q.Get("request_sig") != hex.EncodeToString(sum[:]) {
		t.Error("app_id/request_ts/request_sig leaked into the signed payload")
	}
}

// A value with URL-significant characters must be encoded in the URL but signed
// raw — signing the encoded form would disagree with what the server verifies.
func TestValueIsSignedRawButEncodedInURL(t *testing.T) {
	raw := signedQobuzURL("track/search", map[string]string{"query": "a b&c"})
	if !strings.Contains(raw, "query=a+b%26c") && !strings.Contains(raw, "query=a%20b%26c") {
		t.Errorf("value was not URL-encoded in the query string: %s", raw)
	}
	q, _ := url.ParseQuery(strings.SplitN(raw, "?", 2)[1])
	ts := q.Get("request_ts")
	want := fmt.Sprintf("tracksearch"+"query"+"a b&c"+"%s"+signedSecret, ts)
	sum := md5.Sum([]byte(want))
	if q.Get("request_sig") != hex.EncodeToString(sum[:]) {
		t.Error("the raw (decoded) value was not what got signed")
	}
}
