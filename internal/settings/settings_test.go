package settings

import "testing"

func TestParseDownloadSettingsDefaultsOnMissingKeys(t *testing.T) {
	got := ParseDownloadSettings(nil)
	want := DownloadSettings{
		// TidalQuality/QobuzQuality are never bare zero values — they run
		// through backend.TidalQualityFor/QobuzQualityFor, whose default
		// case for an unrecognized (here: empty) input is LOSSLESS/"6".
		TidalQuality: "LOSSLESS",
		QobuzQuality: "6",
	}
	if got != want {
		t.Errorf("ParseDownloadSettings(nil) = %+v, want %+v", got, want)
	}
}

func TestParseDownloadSettingsIgnoresWrongTypedValues(t *testing.T) {
	// A corrupted blob (wrong JSON type for a known key) must degrade to the
	// zero value, not panic on a failed type assertion.
	raw := map[string]interface{}{
		"downloadPath":   42,         // not a string
		"embedLyrics":    "yes",      // not a bool
		"createM3u8File": []string{}, // not a bool
	}
	got := ParseDownloadSettings(raw)
	if got.DownloadPath != "" {
		t.Errorf("DownloadPath = %q, want empty on type mismatch", got.DownloadPath)
	}
	if got.EmbedLyrics {
		t.Errorf("EmbedLyrics = true, want false on type mismatch")
	}
	if got.CreateM3u8File {
		t.Errorf("CreateM3u8File = true, want false on type mismatch")
	}
}

func TestParseDownloadSettingsNormalizesQuality(t *testing.T) {
	raw := map[string]interface{}{
		"tidalQuality": "garbage",
		"qobuzQuality": "garbage",
	}
	got := ParseDownloadSettings(raw)
	if got.TidalQuality != "LOSSLESS" {
		t.Errorf("TidalQuality = %q, want LOSSLESS (normalized default)", got.TidalQuality)
	}
	if got.QobuzQuality != "6" {
		t.Errorf("QobuzQuality = %q, want 6 (normalized default)", got.QobuzQuality)
	}
}

func TestParseDownloadSettingsPassesThroughValidValues(t *testing.T) {
	raw := map[string]interface{}{
		"downloadPath":         "/music",
		"filenameTemplate":     "{title} - {artist}",
		"tidalQuality":         "HI_RES_LOSSLESS",
		"qobuzQuality":         "27",
		"autoOrder":            "tidal-amazon-qobuz",
		"embedLyrics":          true,
		"embedMaxQualityCover": true,
		"allowFallback":        true,
		"useFirstArtistOnly":   true,
		"useSingleGenre":       true,
		"embedGenre":           true,
		"trackNumber":          true,
		"spotFetchAPIUrl":      "https://example.com/api",
		"createM3u8File":       true,
		"jellyfinMusicPath":    "/jellyfin/music",
	}
	want := DownloadSettings{
		DownloadPath:         "/music",
		FilenameTemplate:     "{title} - {artist}",
		TidalQuality:         "HI_RES_LOSSLESS",
		QobuzQuality:         "27",
		AutoOrder:            "tidal-amazon-qobuz",
		EmbedLyrics:          true,
		EmbedMaxQualityCover: true,
		AllowFallback:        true,
		UseFirstArtistOnly:   true,
		UseSingleGenre:       true,
		EmbedGenre:           true,
		TrackNumber:          true,
		SpotFetchAPIURL:      "https://example.com/api",
		CreateM3u8File:       true,
		JellyfinMusicPath:    "/jellyfin/music",
	}
	if got := ParseDownloadSettings(raw); got != want {
		t.Errorf("ParseDownloadSettings(raw) = %+v, want %+v", got, want)
	}
}

// TestEffectiveDownloadSettingsPrefersPerUserOverGlobal is the regression
// test for the R8 bug: libraryRoot, the download-request settings filler
// (ApplySettingsFallbacks, since removed as vestigial) and the
// spotFetchAPIUrl readers used to call SystemService.LoadSettings()
// directly, ignoring a signed-in user's own saved settings entirely — even
// though GET /api/v1/settings already correctly returned them. A user who
// saved a custom downloadPath would have it silently ignored by every path
// confinement check. EffectiveDownloadSettings is the single resolver that
// now backs all of those call sites.
func TestEffectiveDownloadSettingsPrefersPerUserOverGlobal(t *testing.T) {
	am := newTestAuthManager(t)

	if err := am.SaveUserSettings("u1", map[string]interface{}{
		"downloadPath": "/home/u1/Music",
	}); err != nil {
		t.Fatalf("auth.SaveUserSettings: %v", err)
	}

	got := EffectiveDownloadSettings(am, "u1")
	if got.DownloadPath != "/home/u1/Music" {
		t.Errorf("DownloadPath = %q, want the user's own saved path (per-user settings must win over global)", got.DownloadPath)
	}
}

// TestEffectiveDownloadSettingsFallsBackWithoutUser covers the two cases
// that must still resolve to the global settings: no authenticated user
// (userID == "") and an authenticated user who has never saved their own
// settings. Neither must ever dereference a nil AuthManager. HOME is
// isolated to a fresh temp dir so the global fallback (config.json under
// ~/.SpotiFLAC) can't read real state left by another test or a developer's
// actual SpotiFLAC install.
func TestEffectiveDownloadSettingsFallsBackWithoutUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := EffectiveDownloadSettings(nil, ""); got.TidalQuality == "" {
		t.Errorf("EffectiveDownloadSettings(nil, \"\") did not resolve a default TidalQuality")
	}

	am := newTestAuthManager(t)
	got := EffectiveDownloadSettings(am, "no-settings-saved-yet")
	if got.TidalQuality != "LOSSLESS" {
		t.Errorf("TidalQuality = %q, want the global default for a user with no saved settings", got.TidalQuality)
	}
}
