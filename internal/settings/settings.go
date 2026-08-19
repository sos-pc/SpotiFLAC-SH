package settings

import (
	"github.com/sos-pc/SpotiFLAC-SH/backend"
	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/config"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
)

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
// generation — so every backend reader shares one
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
	Downloader           string
	FolderTemplate       string
	CreatePlaylistFolder bool
	FilenameTemplate     string
	TidalQuality         string
	QobuzQuality         string
	AutoOrder            string
	AutoQuality          string
	EmbedLyrics          bool
	EmbedMaxQualityCover bool
	AllowFallback        bool
	UseFirstArtistOnly   bool
	UseSingleGenre       bool
	EmbedGenre           bool
	TrackNumber          bool
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
		Downloader:           getString("downloader"),
		FolderTemplate:       getString("folderTemplate"),
		CreatePlaylistFolder: getBool("createPlaylistFolder"),
		FilenameTemplate:     getString("filenameTemplate"),
		TidalQuality:         backend.TidalQualityFor(getString("tidalQuality")),
		QobuzQuality:         backend.QobuzQualityFor(getString("qobuzQuality")),
		AutoOrder:            getString("autoOrder"),
		AutoQuality:          getString("autoQuality"),
		EmbedLyrics:          getBool("embedLyrics"),
		EmbedMaxQualityCover: getBool("embedMaxQualityCover"),
		AllowFallback:        getBool("allowFallback"),
		UseFirstArtistOnly:   getBool("useFirstArtistOnly"),
		UseSingleGenre:       getBool("useSingleGenre"),
		EmbedGenre:           getBool("embedGenre"),
		TrackNumber:          getBool("trackNumber"),
		CreateM3u8File:       getBool("createM3u8File"),
		JellyfinMusicPath:    getString("jellyfinMusicPath"),
	}
}

// ServerJobSettings builds the JobSettings a download should run with under the
// backend-authoritative model (docs/settings-source-of-truth.md): every field
// comes from the user's saved server settings, and the ONLY per-download
// override honoured is the service (the UI's Source selector). The frontend no
// longer needs to send any of these — the server is the single source of truth.
func ServerJobSettings(s DownloadSettings, serviceOverride string) jobs.JobSettings {
	return jobs.JobSettings{
		Service:              serviceOverride,
		DownloadPath:         s.DownloadPath,
		FolderTemplate:       s.FolderTemplate,
		CreatePlaylistFolder: s.CreatePlaylistFolder,
		FilenameTemplate:     s.FilenameTemplate,
		TrackNumber:          s.TrackNumber,
		EmbedLyrics:          s.EmbedLyrics,
		EmbedMaxQualityCover: s.EmbedMaxQualityCover,
		TidalQuality:         s.TidalQuality,
		QobuzQuality:         s.QobuzQuality,
		AutoOrder:            s.AutoOrder,
		AutoQuality:          s.AutoQuality,
		UseFirstArtistOnly:   s.UseFirstArtistOnly,
		UseSingleGenre:       s.UseSingleGenre,
		EmbedGenre:           s.EmbedGenre,
		AllowFallback:        s.AllowFallback,
		Region:               "", // not a persisted setting; unused on the manual path
	}
}

// EffectiveDownloadSettings resolves the settings that should govern
// behavior for userID: that user's own saved settings (UserProfile.Settings
// in BoltDB) if they have any, else the operator's global config.json.
// userID == "" (no authenticated user — a background caller, or a
// system-wide operation with no single user to attribute) always resolves
// to the global settings.
//
// This is the single resolution point for a per-user-then-global pattern
// that used to be duplicated inline 4x in watcher.go, and — worse — was
// silently skipped by libraryRoot, ApplySettingsFallbacks and both readers of
// the SpotFetch fallback URL (since removed), which read the global file
// unconditionally regardless of which user made the request. An authenticated
// user's own downloadPath was therefore correctly saved and correctly returned
// by GET /api/v1/settings, yet silently ignored by those four call sites in
// favor of the operator's global value.
func EffectiveDownloadSettings(auth *auth.AuthManager, userID string) DownloadSettings {
	return ParseDownloadSettings(EffectiveBlob(auth, userID))
}

// defaultBlob is the value a deployment starts from, before the operator or
// anyone else has saved anything.
//
// Only one entry, and that is not an oversight: every other key's intended
// default IS its zero value, and the substitutions that turn an empty template
// into "title-artist" live downstream on purpose — they guard persisted job
// records, not this resolution (see backend/util's default constants).
//
// createM3u8File is different because its intended default is true, which the
// old design could not express at all: settings arrived as an untyped map and a
// missing key read as false, so "on unless told otherwise" was unreachable.
func defaultBlob() map[string]interface{} {
	return map[string]interface{}{
		"createM3u8File": true,
	}
}

// EffectiveBlob resolves the settings governing userID, as layers:
//
//	defaults  ←  instance store  ←  the user's own patch (user-scoped keys only)
//
// Each layer overrides only the keys it actually sets. That is the whole point,
// and it is what the previous version did not do: it took the user's map
// *instead of* the instance one whenever the user had saved anything at all, so
// a single stored key sent every other setting to its zero value. Nothing had
// noticed because the settings screen always sends the blob back whole — the
// system was correct only for as long as the client kept being polite.
//
// The instance layer applies to every key, not just instance-scoped ones, so an
// operator can set a house-wide default for a personal setting and have it
// reach anyone who has not chosen otherwise. The user layer is filtered to
// user-scoped keys, which is what makes a stale per-user downloadPath — every
// profile written before this shipped has one — stop being honoured.
func EffectiveBlob(am *auth.AuthManager, userID string) map[string]interface{} {
	merged := defaultBlob()

	if instance, err := config.LoadSettingsFile(); err == nil {
		for k, v := range instance {
			merged[k] = v
		}
	}

	if userID != "" && am != nil {
		if profile, err := am.GetUser(userID); err == nil && profile != nil {
			for k, v := range profile.Settings {
				if ScopeOf(k) == ScopeUser {
					merged[k] = v
				}
			}
		}
	}
	return merged
}
