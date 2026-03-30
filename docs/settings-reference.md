# Settings Reference

Settings are per-user and stored in BoltDB. They can be changed in **Settings** in the UI or via the API (`GET/PUT /api/v1/settings`).

---

## Download

### Service / Source priority

| Setting | Values | Default | Description |
|---------|--------|---------|-------------|
| `service` | `auto` `tidal` `qobuz` `amazon` `deezer` | `auto` | Which lossless source to use. `auto` tries Tidal → Qobuz → Amazon → Deezer in order |

When `auto` is set, SpotiFLAC falls through each provider until one succeeds. If all fail, the job is marked failed and can be retried.

### Quality

| Setting | Values | Description |
|---------|--------|-------------|
| `tidal_quality` | `LOSSLESS` `HI_RES` `HI_RES_LOSSLESS` | Tidal quality tier (requires PKCE token for HI_RES) |
| `qobuz_quality` | `27` `6` `7` | Qobuz quality ID (6 = FLAC 16-bit, 7 = FLAC 24-bit, 27 = MP3 320) |

### Paths & filenames

| Setting | Example | Description |
|---------|---------|-------------|
| `download_path` | `/home/nonroot/Music` | Root download directory |
| `filename_format` | `{artist} - {title}` | Filename template (see template variables below) |
| `folder_format` | `{album_artist}/{album}` | Subfolder structure relative to `download_path` |
| `create_playlist_folder` | `true` | Create a subfolder per playlist when downloading playlists |
| `include_track_number` | `false` | Prepend track number to filename (e.g. `01. ...`) |

#### Filename template variables

| Variable | Description |
|----------|-------------|
| `{title}` | Track title |
| `{artist}` | Track artist(s) |
| `{album}` | Album name |
| `{album_artist}` | Album artist |
| `{year}` | 4-digit release year |
| `{date}` | Full release date (`YYYY-MM-DD`) |
| `{track}` | Track number (zero-padded to 2 digits) |
| `{disc}` | Disc number |

Examples:
```
{artist} - {title}                    →  The Beatles - Come Together.flac
{album_artist}/{album}/{track}. {title} →  The Beatles/Abbey Road/02. Something.flac
{year}/{album}/{artist} - {title}     →  1969/Abbey Road/The Beatles - Come Together.flac
```

---

## Metadata & Tags

| Setting | Default | Description |
|---------|---------|-------------|
| `embed_genre` | `false` | Fetch genre from MusicBrainz and embed in tags |
| `use_single_genre` | `false` | When embedding genre, keep only the primary genre |
| `use_first_artist_only` | `false` | Use only the first artist in filename and tags (avoids long multi-artist filenames) |
| `embed_max_quality_cover` | `false` | Fetch the highest-resolution album art available (larger files) |
| `embed_lyrics` | `false` | Embed synchronized lyrics (LRC format via LRCLIB) |

---

## Watchlist defaults

These become the default settings when creating a new watchlist (each watchlist can override them individually):

| Setting | Description |
|---------|-------------|
| `service` | Default download source for new watchlists |
| `download_path` | Default output folder |
| `filename_format` | Default filename template |
| `include_track_number` | Default track number prefix |
| `sync_deletions` | Default for "delete local files when removed from playlist" |

---

## SpotFetch (custom Spotify metadata endpoint)

| Setting | Default | Description |
|---------|---------|-------------|
| `use_spot_fetch_api` | `false` | Use a custom Spotify metadata API instead of the built-in client |
| `spot_fetch_api_url` | *(empty)* | Base URL of the SpotFetch-compatible API |

SpotFetch is useful when the built-in Spotify scraping is blocked. See the SpotFetch project for self-hosting instructions.

---

## API

### `GET /api/v1/settings`

```json
{
  "service": "auto",
  "download_path": "/home/nonroot/Music",
  "filename_format": "{artist} - {title}",
  "folder_format": "{album_artist}/{album}",
  "tidal_quality": "LOSSLESS",
  "embed_genre": false,
  "use_single_genre": false,
  "use_first_artist_only": false,
  "embed_max_quality_cover": false,
  "embed_lyrics": false,
  "include_track_number": false,
  "create_playlist_folder": true,
  "use_spot_fetch_api": false,
  "spot_fetch_api_url": ""
}
```

### `PUT /api/v1/settings`

Send the full settings object (or a partial — unknown keys are ignored). Returns `204`.
