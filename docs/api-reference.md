# API Reference

All endpoints are under `/api/v1/`. Every request (except `/auth/login` and `/auth/local`) requires authentication via a JWT Bearer token or an API key.

## Authentication

| Method | Header | Value |
|--------|--------|-------|
| Bearer token | `Authorization` | `Bearer <jwt>` |
| API key | `X-API-Key` | `sk_spotiflac_<key>` |

On `401`, the session has expired — re-authenticate.

---

## Auth

### `POST /api/v1/auth/login`
Authenticate with Jellyfin credentials.

**Body**
```json
{ "username": "alice", "password": "secret" }
```
**Response `200`**
```json
{ "token": "<jwt>", "user": { "id": "...", "name": "alice", "is_admin": true } }
```
**Response `429`** — rate limited (5 failed attempts). Wait 5 minutes.

---

### `POST /api/v1/auth/local`
Auto-login on LAN when `DISABLE_AUTH_ON_LAN=true`. Returns admin JWT with no credentials required. Returns `403` if the feature is disabled or the request comes through a reverse proxy.

---

### `GET /api/v1/auth/me`
Returns the current user's profile.

```json
{ "id": "...", "name": "alice", "display_name": "Alice", "is_admin": true }
```

---

### API Keys

#### `GET /api/v1/auth/keys`
List your API keys (key values are masked).

```json
[
  { "id": "abc", "name": "my-app", "permissions": ["download"], "created_at": "2026-01-01T00:00:00Z" }
]
```

#### `POST /api/v1/auth/keys`
Create a new API key. The `key` field is returned **once only** — save it immediately.

**Body**
```json
{ "name": "my-app", "permissions": ["download"] }
```
**Response `200`**
```json
{ "id": "abc", "name": "my-app", "key": "sk_spotiflac_e4a9d596..." }
```

#### `DELETE /api/v1/auth/keys/{id}`
Revoke an API key. Returns `204`.

---

### Tidal Auth (Device Code)

#### `POST /api/v1/auth/tidal/device/start`
Start the OAuth Device Code flow. Returns the authorization URL and device code.

```json
{
  "device_code": "abc123...",
  "user_code": "LDANN",
  "verification_uri_complete": "https://link.tidal.com/LDANN",
  "expires_in": 300,
  "interval": 5
}
```

#### `POST /api/v1/auth/tidal/device/poll`
Poll for authorization status. Call every `interval` seconds with the `device_code` from the start response.

**Body**
```json
{ "device_code": "abc123..." }
```

**Response**
```json
{ "status": "pending" }
{ "status": "authorized" }
{ "status": "expired", "error": "Authorization expired. Please start again." }
```

Statuses: `pending` · `authorized` · `expired` · `denied` · `error`

#### `GET /api/v1/auth/tidal/status`
Returns current Tidal token status.

```json
{ "connected": true, "expires_at": 1753920000 }
```

#### `DELETE /api/v1/auth/tidal`
Disconnect Tidal account. Returns `204`.

---

## Search & Metadata

### `GET /api/v1/search?url={spotifyURL}&batch={bool}`
Resolve a Spotify URL (track, album, playlist, artist) and return full metadata.

```json
{
  "type": "album",
  "name": "Abbey Road",
  "artist": "The Beatles",
  "tracks": [ { "id": "...", "name": "Come Together", "duration_ms": 259000 } ]
}
```

### `GET /api/v1/search/query?q={query}&type={type}&limit={n}&offset={n}`
Search Spotify. `type` can be `track`, `album`, `artist`, `playlist`.

### `GET /api/v1/tracks/{id}/preview`
Returns a 30-second preview URL.

```json
{ "url": "https://p.scdn.co/mp3-preview/..." }
```

### `GET /api/v1/tracks/{id}/availability`
Check which lossless platforms have this track.

```json
{ "tidal": true, "qobuz": false, "amazon": true }
```

### `GET /api/v1/tracks/{id}/links?region={region}`
Get streaming URLs for a track on all supported platforms.

---

## Downloads

### `POST /api/v1/downloads/track`
Download a single track immediately (bypasses queue for one-off use).

**Body**
```json
{
  "spotify_id": "4uLU6hMCjMI75M1A2tKUQC",
  "output_dir": "/home/nonroot/Music",
  "service": "auto"
}
```

### `POST /api/v1/jobs`
Enqueue a batch download (album, playlist, artist).

**Body**
```json
{
  "tracks": [ { "spotify_id": "...", "track_name": "...", "artist_name": "..." } ],
  "output_dir": "/home/nonroot/Music",
  "service": "auto",
  "filename_format": "{artist} - {title}",
  "quality": "LOSSLESS"
}
```
**Response `200`**
```json
{ "enqueued": 12, "skipped": 2 }
```

### `GET /api/v1/jobs`
List your current download queue items.

### `GET /api/v1/jobs/stream`
Server-Sent Events (SSE) stream for real-time download progress. Connect with `EventSource`.

```
data: {"id":"abc","status":"downloading","progress":42,"speed_bps":1200000}
```

### `GET /api/v1/jobs/progress`
Snapshot of current overall queue progress.

### `DELETE /api/v1/jobs`
Clear all jobs. Admin clears all users; regular user clears only their own.

### `DELETE /api/v1/jobs/completed`
Remove completed jobs from the queue.

### `DELETE /api/v1/jobs/pending`
Cancel all pending (not yet started) jobs.

---

## Watchlists

Watchlists monitor Spotify playlists and automatically download new tracks at a configured interval.

### `GET /api/v1/watchlists`
List all your watchlists.

### `POST /api/v1/watchlists`
Add a playlist to watch.

**Body**
```json
{
  "spotify_url": "https://open.spotify.com/playlist/...",
  "interval_hours": 6,
  "output_dir": "/home/nonroot/Music/Playlists",
  "service": "auto",
  "sync_deletions": false
}
```

### `PUT /api/v1/watchlists/{id}`
Update a watchlist's settings (interval, sync_deletions, etc.).

### `DELETE /api/v1/watchlists/{id}`
Remove a watchlist (does not delete downloaded files).

### `POST /api/v1/watchlists/{id}/sync`
Trigger an immediate sync (downloads new tracks + retries failed ones).

### `GET /api/v1/watchlists/{id}/stats`
```json
{ "total": 48, "downloaded": 45, "missing": 3, "failed": 1 }
```

### `GET /api/v1/watchlists/{id}/history`
List of sync log entries (timestamp, new_tracks, downloaded, skipped, failed).

---

## History

### `GET /api/v1/history/downloads`
Your download history.

### `DELETE /api/v1/history/downloads`
Clear all download history.

### `DELETE /api/v1/history/downloads/{id}`
Delete a single history entry.

### `GET /api/v1/history/downloads/export`
Export failed downloads as a text list.

### `GET /api/v1/history/fetch`
Spotify URL fetch history (recent searches).

### `POST /api/v1/history/fetch`
Add an entry to fetch history.

### `DELETE /api/v1/history/fetch`
Clear fetch history.

### `DELETE /api/v1/history/fetch?type={type}`
Clear fetch history by type (`track`, `album`, `playlist`, `artist`).

### `DELETE /api/v1/history/fetch/{id}`
Delete a single fetch history entry.

---

## Settings

### `GET /api/v1/settings`
Get your current settings.

### `PUT /api/v1/settings`
Save settings. See [Settings Reference](settings-reference.md) for all fields.

---

## Files (Admin only)

### `GET /api/v1/files?path={dir}`
List files in a directory.

### `GET /api/v1/files/audio?path={dir}`
List audio files in a directory with metadata.

### `GET /api/v1/files/metadata?path={file}`
Read embedded tags from an audio file.

### `GET /api/v1/files/image?path={file}`
Return a file as base64 image data.

### `POST /api/v1/files/read`
Read a text file.

**Body** `{ "file_path": "/path/to/file" }`

### `POST /api/v1/files/rename`
Rename a single file.

**Body** `{ "old_path": "/path/old.flac", "new_name": "new.flac" }`

### `POST /api/v1/files/rename/batch`
Rename multiple files using a format template.

**Body** `{ "files": ["/path/a.flac"], "format": "{artist} - {title}" }`

### `POST /api/v1/files/rename/preview`
Preview batch rename without applying changes.

### `POST /api/v1/files/sizes`
Get file sizes.

**Body** `{ "file_paths": ["/path/a.flac", "/path/b.flac"] }`

### `POST /api/v1/files/exists`
Check which expected files are already on disk.

### `POST /api/v1/files/m3u8`
Generate an M3U8 playlist file.

**Body**
```json
{
  "m3u8_name": "my-playlist",
  "output_dir": "/home/nonroot/Music",
  "file_paths": ["/path/a.flac"],
  "jellyfin_music_path": "/media/music"
}
```

### `POST /api/v1/files/upload/image`
Upload an image via base64.

### `POST /api/v1/files/upload/path`
Serve a local file path as an uploaded image URL.

---

## Audio

### `POST /api/v1/audio/analyze`
Analyze codec, bitrate, sample rate of a single file.

**Body** `{ "file_path": "/path/to/file.flac" }`

### `POST /api/v1/audio/analyze/batch`
Analyze multiple files.

**Body** `{ "file_paths": ["/path/a.flac", "/path/b.flac"] }`

### `POST /api/v1/audio/convert`
Convert audio to another format.

**Body** `{ "file_path": "/path/in.flac", "output_format": "mp3", "bitrate": "320k" }`

---

## Media

### `POST /api/v1/media/lyrics`
Fetch synchronized or plain lyrics via LRCLIB.

### `POST /api/v1/media/cover`
Download album art.

### `POST /api/v1/media/header`
Download artist header image.

### `POST /api/v1/media/gallery`
Download artist gallery images.

### `POST /api/v1/media/avatar`
Download artist avatar.

---

## System

### `GET /api/v1/system/info`
Returns OS, config path, home directory, app version.

```json
{ "os": "linux", "config_path": "/home/nonroot/.SpotiFLAC", "home_dir": "/home/nonroot", "version": "3.3.0" }
```

### `GET /api/v1/system/ffmpeg`
FFmpeg/ffprobe availability and path. FFmpeg is bundled in the Docker image and always present in normal deployments.

```json
{ "installed": true, "ffprobe_installed": true, "ffmpeg_path": "/usr/bin/ffmpeg" }
```

### `GET /api/v1/system/defaults`
Returns default download path, filename format, etc.

---

## APIs

### `GET /api/v1/apis/status`
Health check of all external services (cached 30 seconds).

```json
[
  { "name": "Tidal (triton.squid.wtf)", "url": "https://triton.squid.wtf", "status": "ok", "latency_ms": 45, "checked_at": 1753920000 },
  { "name": "Song.link", "url": "https://api.song.link", "status": "ratelimited", "checked_at": 1753920000 },
  { "name": "Amazon Music proxy", "url": "https://amzn.afkarxyz.fun", "status": "down", "error": "connection refused", "checked_at": 1753920000 }
]
```

**Status values:** `ok` · `down` · `ratelimited` · `unconfigured`

### `GET /api/v1/apis/proxies`
Current proxy configuration.

```json
{
  "tidal_proxies": ["https://triton.squid.wtf", "https://api.monochrome.tf"],
  "qobuz_providers": ["https://dab.yeet.su/api/stream?trackId="],
  "amazon_proxies": ["https://amzn.afkarxyz.fun"],
  "deezer_proxies": ["https://api.deezmate.com"]
}
```

### `PUT /api/v1/apis/proxies`
Update proxy configuration. Applies immediately without restart. Empty lists fall back to built-in defaults.

---

## Error Format

All errors follow the same shape:

```json
{ "error": "human-readable message" }
```

Common HTTP status codes:

| Code | Meaning |
|------|---------|
| `400` | Bad request / missing parameter |
| `401` | Not authenticated or token expired |
| `403` | Insufficient permissions (admin required) |
| `404` | Resource not found |
| `429` | Rate limited |
| `500` | Internal server error |

---

## curl Examples

```bash
BASE=http://spotiflac.example.com
TOKEN=<your-jwt>

# Login
curl -s -X POST $BASE/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret"}' | jq .token

# Search a Spotify album
curl -s "$BASE/api/v1/search?url=https://open.spotify.com/album/..." \
  -H "Authorization: Bearer $TOKEN" | jq .name

# Enqueue a batch download with an API key
curl -s -X POST $BASE/api/v1/jobs \
  -H "X-API-Key: sk_spotiflac_e4a9d596..." \
  -H "Content-Type: application/json" \
  -d '{"tracks":[{"spotify_id":"4uLU6hMCjMI75M1A2tKUQC","track_name":"Come Together","artist_name":"The Beatles"}],"output_dir":"/home/nonroot/Music","service":"auto"}'

# Check proxy/service status
curl -s $BASE/api/v1/apis/status \
  -H "Authorization: Bearer $TOKEN" | jq '.[] | select(.status != "ok")'
```
