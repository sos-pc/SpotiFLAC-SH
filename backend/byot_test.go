package backend

import "testing"

// byotConfigured decides which path runs FIRST. Getting it wrong is not a crash,
// it is a silent downgrade: answer false for a token holder and every Tidal
// download goes to the engine's anonymous access, which returns "proxy HTTP 401 /
// no Tidal APIs configured" on tracks the token can fetch (observed in prod
// 2026-07-28). So the negative cases matter as much as the positive one.
func TestByotConfiguredOnlyAppliesToTidal(t *testing.T) {
	// No HOME manipulation here: whatever LoadTidalToken finds, these providers
	// must never be treated as credential-backed, because they have no native
	// path left to prefer.
	for _, svc := range []string{"qobuz", "amazon", "deezer", "", "auto", "unknown"} {
		if byotConfigured(svc) {
			t.Errorf("byotConfigured(%q) = true; only tidal can carry credentials", svc)
		}
	}
}

// The name is matched the same tolerant way the rest of the dispatch matches it,
// so a chain entry written " Tidal " is not silently treated as tokenless.
func TestByotConfiguredMatchesTidalTolerantly(t *testing.T) {
	plain := byotConfigured("tidal")
	for _, variant := range []string{"Tidal", "TIDAL", " tidal ", "\ttidal"} {
		if got := byotConfigured(variant); got != plain {
			t.Errorf("byotConfigured(%q) = %v, want %v (same as %q)", variant, got, plain, "tidal")
		}
	}
}
