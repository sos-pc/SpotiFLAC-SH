package spotify

import (
	"testing"
	"time"
)

// The process-wide token cache replaced a per-instance field (upstream-catchup
// §S8). These cover the two ways that change can go wrong: serving a token past
// its life, and serving one Spotify has already rejected.

func withCleanTokenCache(t *testing.T) {
	t.Helper()
	invalidateSpotifyTokenCache()
	t.Cleanup(invalidateSpotifyTokenCache)
}

func TestTokenCacheRejectsIncompleteEntries(t *testing.T) {
	withCleanTokenCache(t)
	now := time.Now()

	if spotifyTokenCacheValidLocked(now) {
		t.Fatal("an empty cache must not be considered valid")
	}

	// A token without its clientID is unusable: Query sends both.
	spotifyTokenCache.accessToken = "tok"
	spotifyTokenCache.expiresAt = now.Add(time.Hour)
	if spotifyTokenCacheValidLocked(now) {
		t.Error("a cache entry missing clientID must not be considered valid")
	}
}

func TestTokenCacheExpiresEarlyBySkew(t *testing.T) {
	withCleanTokenCache(t)
	now := time.Now()
	spotifyTokenCache.accessToken = "tok"
	spotifyTokenCache.clientID = "cid"

	// Comfortably inside its life.
	spotifyTokenCache.expiresAt = now.Add(10 * time.Minute)
	if !spotifyTokenCacheValidLocked(now) {
		t.Error("a token with 10 minutes left must be served from cache")
	}

	// Still unexpired, but inside the skew window: a request starting now could
	// land past the boundary, so it must be refreshed instead.
	spotifyTokenCache.expiresAt = now.Add(spotifyTokenCacheSkew / 2)
	if spotifyTokenCacheValidLocked(now) {
		t.Error("a token expiring within the skew window must not be reused")
	}

	spotifyTokenCache.expiresAt = now.Add(-time.Second)
	if spotifyTokenCacheValidLocked(now) {
		t.Error("an expired token must not be reused")
	}
}

// A 401 means Spotify rejected the token. Without this, the refresh that
// follows would read the rejected token straight back out of the shared cache
// and loop.
func TestInvalidateClearsEveryField(t *testing.T) {
	withCleanTokenCache(t)
	spotifyTokenCache.accessToken = "tok"
	spotifyTokenCache.clientID = "cid"
	spotifyTokenCache.expiresAt = time.Now().Add(time.Hour)

	invalidateSpotifyTokenCache()

	if spotifyTokenCacheValidLocked(time.Now()) {
		t.Fatal("cache still reports valid after invalidation")
	}
	if spotifyTokenCache.accessToken != "" || spotifyTokenCache.clientID != "" ||
		!spotifyTokenCache.expiresAt.IsZero() {
		t.Errorf("invalidation left residue: %+v", spotifyTokenCache)
	}
}

// The clock-drift retry is only worth anything if adjacent windows actually
// produce different codes — otherwise the three attempts are the same attempt.
func TestTOTPWindowsDifferAcrossRotation(t *testing.T) {
	c := NewSpotifyClient()
	base := time.Now()

	code, _, err := c.generateTOTPAt(base)
	if err != nil {
		t.Fatalf("generateTOTPAt: %v", err)
	}
	prev, _, err := c.generateTOTPAt(base.Add(-30 * time.Second))
	if err != nil {
		t.Fatalf("generateTOTPAt(-30s): %v", err)
	}
	next, _, err := c.generateTOTPAt(base.Add(30 * time.Second))
	if err != nil {
		t.Fatalf("generateTOTPAt(+30s): %v", err)
	}

	// A 30s step is exactly one rotation, so neither neighbour can equal the
	// centre window. (prev and next may coincide with each other only if the
	// generator were period-2, which it is not.)
	if code == prev {
		t.Error("the -30s window produced the same code as now: drift retry is a no-op")
	}
	if code == next {
		t.Error("the +30s window produced the same code as now: drift retry is a no-op")
	}
}
