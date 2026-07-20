package qobuz

import (
	"os"
	"strconv"
	"testing"
)

// Live probe against musicdl.me, our PRIMARY Qobuz provider.
//
// proxy_config.go records it as dead since May 2026 ("500, encrypted body,
// identical with or without a valid key"). Re-measured 2026-07-20: the host
// answers 200 and POST /api/qobuz/download answers 400 — not 500. A 400 means
// the request was processed and rejected as malformed, which is a different
// thing entirely from a server error, and worth re-testing with a real key.
//
// This is the question the whole "external API layer" project hinges on: if
// musicdl.me works, Qobuz needs no community session, no signature, and no
// human challenge at all — the existing code path just works.
//
// Skipped by default: it hits a third-party service. Run explicitly with
//
//	QOBUZ_LIVE_PROBE=<qobuz_track_id> go test ./backend/qobuz/ -run TestMusicDLLive -v
func TestMusicDLLiveProbe(t *testing.T) {
	raw := os.Getenv("QOBUZ_LIVE_PROBE")
	if raw == "" {
		t.Skip("set QOBUZ_LIVE_PROBE=<qobuz track id> to run this live probe")
	}
	trackID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("QOBUZ_LIVE_PROBE must be a numeric Qobuz track id: %v", err)
	}

	// Derive the key exactly as production does — this is the thing under test
	// as much as the endpoint is.
	key, err := getQobuzMusicDLDebugKey()
	if err != nil {
		t.Fatalf("key derivation failed: %v", err)
	}
	t.Logf("X-Debug-Key derived OK (%d chars)", len(key))

	q := NewQobuzDownloader()
	url, err := q.DownloadFromMusicDL(trackID, "6")
	if err != nil {
		// The error text carries the HTTP status and a body preview, which is
		// what tells "key rejected" apart from "track unknown".
		t.Fatalf("musicdl.me rejected the request: %v", err)
	}
	if url == "" {
		t.Fatal("musicdl.me returned an empty download URL")
	}
	t.Logf("SUCCESS — musicdl.me returned a download URL (%d chars)", len(url))
}
