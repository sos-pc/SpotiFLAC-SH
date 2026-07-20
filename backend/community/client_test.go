package community

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func probeSigner() Signer {
	return Signer{SessionID: "sid", SessionSecret: "secret", AppVersion: "7.2.0"}
}

func requestTo(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, url, strings.NewReader(`{"id":"1"}`))
	}
}

// A cooldown is not a failure of the request — it must be recognisable as such,
// carry the delay, and repeat the service's own wording, which is written for
// the user ("taking a short break, try again in about 16 minutes").
func TestCooldownIsTypedAndCarriesTheDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "960")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"success":false,"error":"The server is taking a short break. Please try again in about 16 minute(s)."}`))
	}))
	defer server.Close()

	_, err := Do(server.Client(), "Qobuz", probeSigner(), requestTo(server.URL))
	if err == nil {
		t.Fatal("a 503 was reported as success")
	}
	if !IsCooldown(err) {
		t.Fatalf("not recognised as a cooldown: %v", err)
	}
	if IsAuth(err) {
		t.Error("a cooldown was also classified as an auth failure")
	}
	if !strings.Contains(err.Error(), "16 minute") {
		t.Errorf("the service's explanation was lost: %v", err)
	}
}

// 401 must clear the stored credentials — leaving them would produce the same
// rejection on every later request — and must NOT retry: re-verification needs
// a fresh Turnstile challenge, so an immediate retry only repeats the rejection.
func TestAuthFailureClearsCredentialsAndDoesNotRetry(t *testing.T) {
	newTestStore(t)
	if err := Save(&Session{
		InstallID: "install", SessionID: "sid", SessionSecret: "secret",
		ExpiresAt: rfc3339In(6 * time.Hour),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"success":false,"error":"Signed request validation failed."}`))
	}))
	defer server.Close()

	_, err := Do(server.Client(), "Qobuz", probeSigner(), requestTo(server.URL))
	if err == nil {
		t.Fatal("a 401 was reported as success")
	}
	if !IsAuth(err) {
		t.Fatalf("not recognised as an auth failure: %v", err)
	}
	if attempts != 1 {
		t.Errorf("made %d attempts, want exactly 1", attempts)
	}
	// The message must not pick one cause among the three the server conflates.
	for _, want := range []string{"expired", "revoked", "different IP"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %v", want, err)
		}
	}

	after, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if after.SessionID != "" || after.SessionSecret != "" {
		t.Errorf("rejected credentials were kept: %+v", after)
	}
	if after.InstallID != "install" {
		t.Error("the InstallID was dropped along with the credentials")
	}
}

// Each attempt must build and sign a fresh request: a signed request cannot be
// replayed — its body is consumed and its nonce is single-use.
func TestEachRetryIsFreshlySigned(t *testing.T) {
	var nonces []string
	var bodies []int64
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		nonces = append(nonces, r.Header.Get("X-Sig-Nonce"))
		bodies = append(bodies, r.ContentLength)
		if attempts < 3 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"url":"https://example/stream.flac"}`))
	}))
	defer server.Close()

	resp, err := Do(server.Client(), "Qobuz", probeSigner(), requestTo(server.URL))
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if attempts != 3 {
		t.Errorf("made %d attempts, want 3", attempts)
	}
	if nonces[0] == nonces[1] || nonces[1] == nonces[2] {
		t.Errorf("a nonce was reused across retries: %v", nonces)
	}
	for i, n := range bodies {
		if n == 0 {
			t.Errorf("attempt %d sent an empty body — it was consumed by the previous signing", i+1)
		}
	}
}

// Statuses about the payload belong to the caller: a 404 for an unknown track
// is an answer, not a failure of this layer, and must not be retried.
func TestPayloadStatusesArePassedThrough(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusNotFound} {
		attempts := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.WriteHeader(status)
		}))

		resp, err := Do(server.Client(), "Qobuz", probeSigner(), requestTo(server.URL))
		if err != nil {
			t.Errorf("status %d turned into an error: %v", status, err)
		} else {
			if resp.StatusCode != status {
				t.Errorf("got %d, want %d", resp.StatusCode, status)
			}
			resp.Body.Close()
		}
		if attempts != 1 {
			t.Errorf("status %d was retried %d times", status, attempts)
		}
		server.Close()
	}
}

// A server asking for an hour must not pin a worker for an hour.
func TestRetryDelayIsCapped(t *testing.T) {
	resp := &http.Response{Header: http.Header{}}
	resp.Header.Set("Retry-After", "6480") // the 108 minutes actually observed
	if got := retryDelay(resp, 0); got > maxWait {
		t.Errorf("delay = %v, want at most %v", got, maxWait)
	}
	// And a missing header still yields a usable, non-zero delay.
	if got := retryDelay(&http.Response{Header: http.Header{}}, 0); got < time.Second {
		t.Errorf("delay = %v, want at least 1s", got)
	}
}
