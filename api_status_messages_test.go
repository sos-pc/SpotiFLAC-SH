package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The UI renders ServiceStatus.Error in a truncating one-line field, so these
// tests pin the two properties that matter: the meaning is stated in plain
// words, and it comes first (before any HTTP number), where truncation can't
// eat it.

func TestDescribeHTTPStatus(t *testing.T) {
	tests := []struct {
		code       int
		wantPrefix string
	}{
		{401, "Access denied"},
		{403, "Access denied"},
		{404, "Not found"},
		{500, "Service is failing"},
		{503, "Service is failing"},
		{418, "Unexpected reply"},
	}
	for _, tt := range tests {
		got := describeHTTPStatus(tt.code)
		if !strings.HasPrefix(got, tt.wantPrefix) {
			t.Errorf("describeHTTPStatus(%d) = %q, want it to start with %q", tt.code, got, tt.wantPrefix)
		}
		// The number stays available, just not in front.
		if tt.code != 404 && !strings.Contains(got, fmt.Sprint(tt.code)) {
			t.Errorf("describeHTTPStatus(%d) = %q, should still mention the code", tt.code, got)
		}
	}
}

func TestDescribeHTTPStatus_LeadsWithMeaningNotNumber(t *testing.T) {
	// Regression guard for what this replaced: a bare "HTTP 503".
	for _, code := range []int{401, 404, 500, 503} {
		got := describeHTTPStatus(code)
		if strings.HasPrefix(got, "HTTP") {
			t.Errorf("describeHTTPStatus(%d) = %q — leads with the raw code again", code, got)
		}
	}
}

func TestDescribeRequestError_DNS(t *testing.T) {
	// A real lookup failure, not a hand-built error value.
	_, err := http.Get("https://this-host-does-not-exist-spotiflac-test.invalid/")
	if err == nil {
		t.Skip("DNS resolved an .invalid host; skipping")
	}
	got := describeRequestError(err)
	if got != "Host not found — check the URL" {
		t.Errorf("describeRequestError(dns) = %q", got)
	}
}

func TestDescribeRequestError_Timeout(t *testing.T) {
	// A server that never answers, against a client that gives up fast.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if got := describeRequestError(err); got != "No response — timed out" {
		t.Errorf("describeRequestError(timeout) = %q", got)
	}
}

func TestDescribeRequestError_ConnectionRefused(t *testing.T) {
	// Bind then immediately close, so the port is guaranteed dead.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	_, err = http.Get("http://" + addr + "/")
	if err == nil {
		t.Skip("something answered on a closed port; skipping")
	}
	got := describeRequestError(err)
	// Windows may reset rather than refuse; both are honest translations.
	if got != "Connection refused — service may be stopped" && got != "Connection dropped by the server" {
		t.Errorf("describeRequestError(refused) = %q", got)
	}
}

func TestDescribeRequestError_DeadlineExceeded(t *testing.T) {
	if got := describeRequestError(context.DeadlineExceeded); got != "No response — timed out" {
		t.Errorf("describeRequestError(DeadlineExceeded) = %q", got)
	}
}

func TestDescribeRequestError_NeverLeaksRawGoError(t *testing.T) {
	// Whatever comes in, the user must not see Go transport jargon.
	for _, err := range []error{
		errors.New(`Get "https://x": dial tcp: lookup x: no such host`),
		errors.New(`Get "https://x": net/http: TLS handshake timeout`),
		errors.New("some entirely unexpected failure"),
	} {
		got := describeRequestError(err)
		for _, leak := range []string{"dial tcp", "net/http", "Get \"", "lookup"} {
			if strings.Contains(got, leak) {
				t.Errorf("describeRequestError(%v) = %q — leaks %q", err, got, leak)
			}
		}
	}
}
