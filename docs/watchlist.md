# Watchlists

Watchlists let SpotiFLAC monitor a Spotify playlist and automatically download new tracks on a schedule — keeping your local library in sync without manual intervention.

---

## How it works

1. You add a Spotify playlist URL as a watchlist.
2. SpotiFLAC resolves the playlist name and current track list from Spotify.
3. At the configured interval, a sync runs:
   - Compares current Spotify tracks against the known list.
   - Queues any new tracks for download.
   - Retries tracks that previously failed.
   - Optionally deletes local files for tracks removed from Spotify.
4. An M3U8 playlist file is regenerated after each sync (if configured).

---

## Adding a watchlist

**Via UI:** Main menu → Watchlists → Add Watchlist → paste Spotify playlist URL.

**Via API:**
```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "spotify_url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
    "interval_hours": 6,
    "output_dir": "/home/nonroot/Music/Playlists/Today Top Hits",
    "service": "auto",
    "sync_deletions": false
  }'
```

---

## Settings per watchlist

Each watchlist has independent settings that override your global defaults:

| Field | Type | Description |
|-------|------|-------------|
| `interval_hours` | int | Sync frequency (minimum: 1h, recommended: 6–24h) |
| `output_dir` | string | Where to save downloaded tracks |
| `service` | string | `auto`, `tidal`, `qobuz`, `amazon`, `deezer` |
| `filename_format` | string | Filename template (see [settings-reference.md](settings-reference.md)) |
| `include_track_number` | bool | Prepend track position in the playlist to filename |
| `sync_deletions` | bool | Delete local files when tracks are removed from Spotify |
| `upgrade_quality` | bool | Re-download existing tracks if a higher quality becomes available |
| `embed_lyrics` | bool | Embed lyrics when downloading |
| `embed_genre` | bool | Fetch genre from MusicBrainz |

---

## Sync behaviour

### New tracks
Any track present in Spotify but not yet downloaded is queued immediately on the next sync.

### Failed tracks
Tracks that failed on a previous sync (network error, provider down, etc.) are automatically retried on every sync. They stay in the "failed" bucket until they succeed or are removed from the playlist.

### Sync deletions
When `sync_deletions: true`, tracks removed from the Spotify playlist are deleted from the local `output_dir`.

**Multi-playlist protection:** A file is only deleted if it is not referenced by any **other** watchlist pointing to the same directory. This prevents a track shared between two playlists from being deleted just because it was removed from one of them.

### M3U8 regeneration
After each sync, SpotiFLAC regenerates the M3U8 playlist file if a Jellyfin music path is configured. This lets Jellyfin pick up changes immediately.

---

## Stats & history

**Stats** (`GET /api/v1/watchlists/{id}/stats`):

```json
{ "total": 50, "downloaded": 47, "missing": 3, "failed": 1 }
```

| Field | Meaning |
|-------|---------|
| `total` | Total tracks currently in the Spotify playlist |
| `downloaded` | Tracks confirmed present on disk |
| `missing` | Total minus downloaded |
| `failed` | Tracks that failed on the last sync attempt |

**History** (`GET /api/v1/watchlists/{id}/history`):

```json
[
  {
    "synced_at": "2026-03-30T10:00:00Z",
    "new_tracks": 3,
    "downloaded": 3,
    "skipped": 44,
    "failed": 0,
    "deleted": 0
  }
]
```

---

## Manual sync

Trigger an immediate sync from the UI (Watchlists → Sync button) or via API:

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists/{id}/sync \
  -H "Authorization: Bearer <token>"
```

Manual sync does the same as a scheduled sync: downloads new tracks + retries failed ones.

---

## Removing a watchlist

Removing a watchlist stops future syncs and deletes the watchlist entry from the database. **It does not delete any downloaded files.**

---

## M3U8 & Jellyfin integration

To generate M3U8 files that Jellyfin can read:

1. In Settings, set **Jellyfin Music Path** to the path Jellyfin uses to mount your music library (e.g. `/media/music` if Jellyfin maps the same folder under that path).
2. SpotiFLAC will generate a `.m3u8` file in `output_dir` after each sync, with paths translated to the Jellyfin mount point.

Example: local file at `/home/nonroot/Music/Playlist/track.flac` → M3U8 entry: `/media/music/Playlist/track.flac`

---

## Tips

- **Keep intervals ≥ 6h** — Spotify's API is rate-limited. Very short intervals can cause failures.
- **Use a dedicated `output_dir` per playlist** to avoid file collisions and make `sync_deletions` safe.
- **Failed tracks are retried automatically** — no need to manually re-queue unless you want to force an immediate retry via the manual sync button.
