# Settings Reference

Settings are **per-user** and stored in BoltDB inside the user's profile. They can be changed in **Settings** in the UI or via the API:

- `GET /api/v1/settings` — returns the current user's settings (or the legacy `config.json` fallback if the user has none yet).
- `PUT /api/v1/settings` — saves the full settings object. Unknown keys are accepted and stored as-is (forward-compatible).

> **Key naming convention.** All keys are **`camelCase`**. The frontend `Settings` interface in `frontend/src/lib/settings.ts` is the source of truth. The Go side stores them as `map[string]interface{}` and only reads a handful of keys directly (`createM3u8File`, `jellyfinMusicPath`, `spotFetchAPIUrl`); the rest pass through to the frontend or are projected into a `JobSettings` struct when a download is enqueued.

The example values in this document match `DEFAULT_SETTINGS` (`frontend/src/lib/settings.ts`).

---

## Download — Source

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `downloader` | `"auto" \| "tidal" \| "qobuz" \| "amazon" \| "deezer"` | `"auto"` | Which lossless source to use. With `auto`, providers are tried in the order set by `autoOrder`. |
| `autoOrder` | string (24 permutations) | `"tidal-qobuz-amazon-deezer"` | Provider fallback order when `downloader = "auto"`. Format is a hyphen-separated permutation of `tidal`, `qobuz`, `amazon`, `deezer`. |
| `autoQuality` | `"16" \| "24"` | `"16"` | Target bit depth when `downloader = "auto"`. `"16"` → `LOSSLESS`. `"24"` → `HI_RES_LOSSLESS`. |
| `allowFallback` | bool | `true` | If a provider fails (proxy down, track not found), continue trying the next one in `autoOrder`. When `false`, a single failure marks the job failed. |

When all providers fail, the job is marked `failed` and can be retried on the next watchlist sync (manual sync only — see [watchlist.md](watchlist.md)).

### Per-provider quality

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `tidalQuality` | `"LOSSLESS" \| "HI_RES_LOSSLESS"` | `"LOSSLESS"` | Tidal quality tier. `HI_RES_LOSSLESS` (24-bit FLAC) requires a Tidal Premium token; community proxies serve preview-only. |
| `qobuzQuality` | `"6" \| "7" \| "27"` | `"6"` | Qobuz quality ID. `6` = 16-bit FLAC, `7` = 24-bit FLAC, `27` = 24-bit Hi-Res. |
| `amazonQuality` | `"original"` | `"original"` | Amazon ships a single quality tier. Reserved for future use. |

> Tidal returns `"HI_RES"` in some contexts; the Go side maps it to `HI_RES_LOSSLESS` for safety. The frontend only exposes the two values above.

---

## Download — Paths & filenames

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `downloadPath` | string | `""` (filled with `GetDefaults().downloadPath`, e.g. `/home/nonroot/Music`) | Root download directory. |
| `folderPreset` | string | `"none"` | One of 15 presets — UI-only, sets `folderTemplate` when a preset is picked. |
| `folderTemplate` | string | `""` | Subfolder structure relative to `downloadPath`. Empty = no subfolders. |
| `filenamePreset` | string | `"title-artist"` | One of 13 presets — UI-only, sets `filenameTemplate`. |
| `filenameTemplate` | string | `"{title} - {artist}"` | Filename template (extension is appended automatically). |
| `trackNumber` | bool | `false` | When `true`, the filename gets a track-number prefix even if the template doesn't include `{track}`. |
| `createPlaylistFolder` | bool | `true` | When downloading a playlist, create a subfolder named after the playlist. Skipped if `folderTemplate` already references `{album}`, `{album_artist}` or `{playlist}`. |

### Folder presets

Each preset applies a fixed `folderTemplate`. Picking a preset overwrites the current template.

| Preset | Template |
|--------|----------|
| `none` | *(empty)* |
| `artist` | `{artist}` |
| `album` | `{album}` |
| `year-album` | `[{year}] {album}` |
| `year-artist-album` | `[{year}] {artist} - {album}` |
| `artist-album` | `{artist}/{album}` |
| `artist-year-album` | `{artist}/[{year}] {album}` |
| `artist-year-nested-album` | `{artist}/{year}/{album}` |
| `album-artist` | `{album_artist}` |
| `album-artist-album` | `{album_artist}/{album}` |
| `album-artist-year-album` | `{album_artist}/[{year}] {album}` |
| `album-artist-year-nested-album` | `{album_artist}/{year}/{album}` |
| `year` | `{year}` |
| `year-artist` | `{year}/{artist}` |
| `custom` | (use the textbox below) |

### Filename presets

| Preset | Template |
|--------|----------|
| `title` | `{title}` |
| `title-artist` | `{title} - {artist}` |
| `artist-title` | `{artist} - {title}` |
| `track-title` | `{track}. {title}` |
| `track-title-artist` | `{track}. {title} - {artist}` |
| `track-artist-title` | `{track}. {artist} - {title}` |
| `title-album-artist` | `{title} - {album_artist}` |
| `track-title-album-artist` | `{track}. {title} - {album_artist}` |
| `artist-album-title` | `{artist} - {album} - {title}` |
| `track-dash-title` | `{track} - {title}` |
| `disc-track-title` | `{disc}-{track}. {title}` |
| `disc-track-title-artist` | `{disc}-{track}. {title} - {artist}` |
| `custom` | (use the textbox below) |

### Template variables

Used in both `folderTemplate` and `filenameTemplate`.

| Variable | Description | Example |
|----------|-------------|---------|
| `{title}` | Track title | `Shake It Off` |
| `{artist}` | Track artist(s). Joined with the original delimiter from Spotify metadata. | `Taylor Swift` |
| `{album}` | Album name | `1989` |
| `{album_artist}` | Album artist (falls back to `{artist}`) | `Taylor Swift` |
| `{track}` | Track number, **zero-padded to 2 digits** in filenames, raw value in folders | `01` / `1` |
| `{disc}` | Disc number | `1` |
| `{year}` | 4-digit release year | `2014` |
| `{date}` | Full release date | `2014-10-27` |
| `{playlist}` | Playlist name (only available in folder templates when downloading a playlist) | `Today's Top Hits` |

> Filenames are sanitized: characters `< > : " / \ | ? *` are replaced and trailing dots/spaces stripped (`util.SanitizeFilename`). Sub-folder boundaries (`/`) inside templates are preserved.

---

## Metadata & Tags

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `embedLyrics` | bool | `false` | Embed synchronized lyrics from LRCLIB (LRC format). |
| `embedMaxQualityCover` | bool | `false` | Fetch the highest-resolution album art available (heavier files). |
| `embedGenre` | bool | `true` | Fetch genre from MusicBrainz and embed in tags. |
| `useSingleGenre` | bool | `false` | When `embedGenre` is on, keep only the primary genre instead of the full list. |
| `useFirstArtistOnly` | bool | `false` | Use only the first artist when building filenames and writing tags (avoids unwieldy multi-artist filenames). Splits on `, ` ` & ` ` feat. ` ` ft. ` ` featuring `. |

---

## M3U8 / Jellyfin integration

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `createM3u8File` | bool | `false` | After every watchlist sync, write `<downloadPath>/Playlists/<sanitized-name>.m3u8`. The file is regenerated atomically (write-then-rename). |
| `jellyfinMusicPath` | string | `""` | When set, every entry in the M3U8 has its `downloadPath` prefix replaced by this path — so Jellyfin (which sees the music library mounted at a different path) can still find the files. Example: local `/home/nonroot/Music/Album/01.flac` → M3U8 `/media/music/Album/01.flac` if `downloadPath = /home/nonroot/Music` and `jellyfinMusicPath = /media/music`. |

> If `createM3u8File` is `false`, no M3U8 is ever written or deleted by the watcher, even when a watchlist is renamed or removed.

---

## SpotFetch (custom Spotify metadata API)

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `spotFetchAPIUrl` | string | `"https://spotify.afkarxyz.fun/api"` | Optional fallback metadata API. Used **only** when the native TOTP-based Spotify scraper fails. The `/apis/status` health-check uses this URL when set. |

The native scraper tries first. SpotFetch is invoked transparently if the native call returns an error — useful when Spotify's web token endpoint is temporarily blocked. Self-hosting the SpotFetch API is documented in its own project.

---

## UI / Theme

These keys are stored alongside everything else in BoltDB but only consumed by the React frontend.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `theme` | string | `"yellow"` | Color theme name (the UI exposes a fixed list, see `frontend/src/lib/themes.ts`). |
| `themeMode` | `"auto" \| "light" \| "dark"` | `"auto"` | Color scheme. `auto` follows the OS preference via `prefers-color-scheme`. |
| `fontFamily` | string | `"google-sans"` | One of 17 web-fonts: `bricolage-grotesque`, `dm-sans`, `figtree`, `geist-sans`, `google-sans`, `inter`, `jetbrains-mono`, `manrope`, `noto-sans`, `nunito-sans`, `outfit`, `plus-jakarta-sans`, `poppins`, `public-sans`, `raleway`, `roboto`, `space-grotesk`. |
| `sfxEnabled` | bool | `true` | Play UI sound effects on download completion / errors. |

---

## Read-only / derived

These appear in the saved blob but are computed automatically — overwriting them has no lasting effect.

| Key | Type | Description |
|-----|------|-------------|
| `operatingSystem` | `"Windows" \| "linux/MacOS"` | Detected from `navigator.platform` on every load. Stored only for the legacy desktop build's UI labels — irrelevant in the web build. |

### Migrated legacy keys

The frontend transparently migrates older settings shapes when loading:

| Old key | New key | Migration |
|---------|---------|-----------|
| `darkMode: bool` | `themeMode: "dark" \| "light"` | `darkMode → themeMode` and old key dropped. |
| `artistSubfolder` / `albumSubfolder` | `folderPreset` + `folderTemplate` | Both true → `artist-album`. Either alone → matching preset. Neither → `none`. |
| `filenameFormat: "title-artist" \| "artist-title" \| "title"` | `filenamePreset` + `filenameTemplate` | Mapped to the closest new preset. |

---

## Watchlist defaults

A watchlist receives its own copy of the relevant settings at creation time (`AddWatchlistRequest.settings`). The values come from the user's current settings via the frontend — there is no separate "watchlist defaults" group. Settings that change per-watchlist behaviour:

- `downloader`, `autoOrder`, `autoQuality`, `tidalQuality`, `qobuzQuality`
- `downloadPath`, `folderTemplate`, `filenameTemplate`, `trackNumber`
- `embedLyrics`, `embedMaxQualityCover`, `embedGenre`, `useSingleGenre`, `useFirstArtistOnly`
- `createPlaylistFolder`, `allowFallback`

The `sync_deletions` flag is a separate per-watchlist boolean (see [watchlist.md](watchlist.md)).

---

## API reference

### `GET /api/v1/settings`

```json
{
  "downloadPath":         "/home/nonroot/Music",
  "downloader":           "auto",
  "theme":                "yellow",
  "themeMode":            "auto",
  "fontFamily":           "google-sans",
  "folderPreset":         "none",
  "folderTemplate":       "",
  "filenamePreset":       "title-artist",
  "filenameTemplate":     "{title} - {artist}",
  "trackNumber":          false,
  "sfxEnabled":           true,
  "embedLyrics":          false,
  "embedMaxQualityCover": false,
  "tidalQuality":         "LOSSLESS",
  "qobuzQuality":         "6",
  "amazonQuality":        "original",
  "autoOrder":            "tidal-qobuz-amazon-deezer",
  "autoQuality":          "16",
  "allowFallback":        true,
  "spotFetchAPIUrl":      "https://spotify.afkarxyz.fun/api",
  "createPlaylistFolder": true,
  "createM3u8File":       false,
  "jellyfinMusicPath":    "",
  "useFirstArtistOnly":   false,
  "useSingleGenre":       false,
  "embedGenre":           true
}
```

### `PUT /api/v1/settings`

Send the full settings object. The handler stores whatever you send under the user's record — there is no validation server-side. Returns `200 { "ok": true }`.

When the user has no per-user settings yet, the handler falls back to the global `config.json` (legacy). The first `PUT` migrates the user to the per-user storage.
