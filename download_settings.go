package main

import "github.com/afkarxyz/SpotiFLAC/backend"

// ─────────────────────────────────────────────────────────────────────────────
// DownloadSettings — typed view over the backend-behavior subset of the
// settings blob (R8).
//
// The full settings blob (map[string]interface{}, stored either in
// config.json or per-user in UserProfile.Settings) is owned by the frontend:
// most of its ~30 keys are pure UI preferences (theme, font, sound effects,
// preset selectors) the backend never interprets and must never lose on a
// round-trip. DownloadSettings types only the ~15 keys the backend actually
// reads to drive behavior — download path, quality, filename/M3U8
// generation, SpotFetch fallback — so every backend reader shares one
// validated, defaulted view instead of five ad hoc getBool/getString call
// sites.
//
// This is a read-side typed VIEW, not a storage format change: the blob
// itself stays a map, PUT /api/v1/settings still accepts and stores it
// whole (including every key DownloadSettings doesn't know about), so
// adding a new frontend-only preference never requires a backend change.
// ─────────────────────────────────────────────────────────────────────────────

type DownloadSettings struct {
	DownloadPath         string
	FolderTemplate       string
	CreatePlaylistFolder bool
	FilenameTemplate     string
	TidalQuality         string
	QobuzQuality         string
	AutoOrder            string
	EmbedLyrics          bool
	EmbedMaxQualityCover bool
	AllowFallback        bool
	UseFirstArtistOnly   bool
	UseSingleGenre       bool
	EmbedGenre           bool
	TrackNumber          bool
	SpotFetchAPIURL      string
	CreateM3u8File       bool
	JellyfinMusicPath    string
}

// ParseDownloadSettings extracts DownloadSettings from a raw settings blob.
// Tolerant of a nil map, missing keys and wrong-typed values — every field
// falls back to its Go zero value exactly like the getBool/getString
// closures it replaces, so this is a pure behavior-preserving consolidation,
// not a stricter re-validation. The raw map is never mutated.
//
// TidalQuality/QobuzQuality are normalized through backend.TidalQualityFor/
// QobuzQualityFor — the same tolerant mapping (invalid or missing -> a safe
// default, never an error) already applied once a download job is built —
// so every reader gets an already-valid quality string instead of relying
// on a later call site to normalize it.
func ParseDownloadSettings(raw map[string]interface{}) DownloadSettings {
	getString := func(key string) string {
		if v, ok := raw[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}
	getBool := func(key string) bool {
		if v, ok := raw[key]; ok {
			if b, ok := v.(bool); ok {
				return b
			}
		}
		return false
	}

	return DownloadSettings{
		DownloadPath:         getString("downloadPath"),
		FolderTemplate:       getString("folderTemplate"),
		CreatePlaylistFolder: getBool("createPlaylistFolder"),
		FilenameTemplate:     getString("filenameTemplate"),
		TidalQuality:         backend.TidalQualityFor(getString("tidalQuality")),
		QobuzQuality:         backend.QobuzQualityFor(getString("qobuzQuality")),
		AutoOrder:            getString("autoOrder"),
		EmbedLyrics:          getBool("embedLyrics"),
		EmbedMaxQualityCover: getBool("embedMaxQualityCover"),
		AllowFallback:        getBool("allowFallback"),
		UseFirstArtistOnly:   getBool("useFirstArtistOnly"),
		UseSingleGenre:       getBool("useSingleGenre"),
		EmbedGenre:           getBool("embedGenre"),
		TrackNumber:          getBool("trackNumber"),
		SpotFetchAPIURL:      getString("spotFetchAPIUrl"),
		CreateM3u8File:       getBool("createM3u8File"),
		JellyfinMusicPath:    getString("jellyfinMusicPath"),
	}
}

// EffectiveDownloadSettings resolves the settings that should govern
// behavior for userID: that user's own saved settings (UserProfile.Settings
// in BoltDB) if they have any, else the operator's global config.json.
// userID == "" (no authenticated user — DISABLE_AUTH_ON_LAN, or a
// system-wide operation with no single user to attribute) always resolves
// to the global settings.
//
// This is the single resolution point for a per-user-then-global pattern
// that used to be duplicated inline 4x in watcher.go, and — worse — was
// silently skipped by libraryRoot, ApplySettingsFallbacks and both
// spotFetchAPIUrl readers, which read the global file unconditionally
// regardless of which user made the request. An authenticated user's own
// downloadPath/spotFetchAPIUrl was therefore correctly saved and correctly
// returned by GET /api/v1/settings, yet silently ignored by those four call
// sites in favor of the operator's global value.
func EffectiveDownloadSettings(auth *AuthManager, userID string) DownloadSettings {
	var raw map[string]interface{}
	if userID != "" && auth != nil {
		if profile, err := auth.GetUser(userID); err == nil && profile != nil && len(profile.Settings) > 0 {
			raw = profile.Settings
		}
	}
	if raw == nil {
		raw, _ = (&SystemService{}).LoadSettings()
	}
	return ParseDownloadSettings(raw)
}
