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
		CreateM3u8File:       true,
		JellyfinMusicPath:    "/jellyfin/music",
	}
	if got := ParseDownloadSettings(raw); got != want {
		t.Errorf("ParseDownloadSettings(raw) = %+v, want %+v", got, want)
	}
}

// TestEffectiveDownloadSettingsPrefersPerUserOverGlobal used to assert that a
// user's own downloadPath won over the instance one. It was the regression test
// for the R8 bug, where four call sites read the global file directly and
// ignored a signed-in user's saved settings entirely.
//
// That fix was right for a single-user deployment and is wrong for a shared
// one. A user's downloadPath is also the root that confines them, so honouring
// it let a non-admin choose their own boundary; and a file's location being
// per-user fragments one shared library into several. downloadPath is now
// instance-scoped, and a stored per-user copy — which every profile written
// before that change has — is ignored on read rather than merely refused on
// write.
//
// The half of R8 that survives is the half that was really about resolution:
// a user's own USER-scoped settings must reach every backend call site, not
// just GET /api/v1/settings. That is what this now asserts.
func TestEffectiveDownloadSettingsAppliesUserScopedKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	am := newTestAuthManager(t)

	if err := am.SaveUserSettings("u1", map[string]interface{}{
		"tidalQuality": "HI_RES_LOSSLESS", // theirs
		"downloadPath": "/home/u1/Music",  // not theirs to set
	}); err != nil {
		t.Fatalf("auth.SaveUserSettings: %v", err)
	}

	got := EffectiveDownloadSettings(am, "u1")
	if got.TidalQuality != "HI_RES_LOSSLESS" {
		t.Errorf("TidalQuality = %q, want the user's own saved value", got.TidalQuality)
	}
	if got.DownloadPath == "/home/u1/Music" {
		t.Error("a stored per-user downloadPath is still honoured; it is instance-scoped now")
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
