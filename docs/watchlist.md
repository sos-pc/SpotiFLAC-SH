# Watchlists

Watchlists let SpotiFLAC monitor a Spotify playlist and automatically download new tracks on a schedule — keeping your local library in sync without manual intervention.

The implementation lives in `watcher.go` (the `Watcher` struct). It owns its own daemon goroutine and shares the `JobManager` queue with manual downloads.

---

## How it works

1. You add a Spotify playlist URL as a watchlist (`POST /api/v1/watchlists`).
2. SpotiFLAC fetches Spotify metadata immediately, resolves the playlist name, stores all current track IDs, and `EnqueueBatch`-es every track for download.
3. The daemon ticks every **5 minutes** and runs `syncPlaylist` for each watchlist whose `lastSync + intervalHours` is in the past:
   - Re-fetches Spotify metadata (5-minute timeout).
   - **Recovers missing files** — `recoverMissingFiles` walks the BoltDB jobs of this watchlist and removes the corresponding `track_id` if the file no longer exists on disk. The next iteration of the same sync will re-queue it.
   - Compares current Spotify tracks against `TrackIDs` and `EnqueueBatch`-es the new ones.
   - Optionally deletes local files for tracks removed from Spotify (`sync_deletions`).
   - Writes a per-sync log entry to `pl.SyncLogs` (capped at the last 20).
   - Regenerates the M3U8 file (when no new download batch is started; otherwise `OnBatchComplete` regenerates it once all jobs of the batch are terminal).
4. Failed tracks of a watchlist are **only retried on manual sync** (`POST /watchlists/{id}/sync`) — the daemon does not re-queue failed jobs to avoid hammering rate-limited proxies.

> The daemon also runs `checkM3U8Integrity` once at server startup for each watchlist: if the M3U8 file count diverges from the on-disk valid jobs, it regenerates the file. This recovers from crashes or external file moves.

---

## Adding a watchlist

**Via UI:** Sidebar → Watchlists → Add Watchlist → paste Spotify playlist URL.

**Via API:**

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "spotify_url": "https://open.spotify.com/playlist/37i9dQZF1DXcBWIGoYBM5M",
    "interval_hours": 6,
    "settings": {
      "service":              "auto",
      "downloadPath":         "/home/nonroot/Music/Playlists/Today Top Hits",
      "filenameTemplate":     "{title} - {artist}",
      "folderTemplate":       "",
      "tidalQuality":         "LOSSLESS",
      "qobuzQuality":         "6",
      "autoOrder":            "tidal-qobuz-amazon-deezer",
      "autoQuality":          "16",
      "embedLyrics":          true,
      "embedGenre":           true,
      "createPlaylistFolder": false,
      "allowFallback":        true
    },
    "sync_deletions": false
  }'
```

If `interval_hours <= 0`, the watcher uses `24`. If `spotify_url` is empty, the request is rejected.

The settings block follows the `JobSettings` shape (camelCase). It is **stored as-is on the watchlist** and used for every download triggered by this watchlist (jobs are re-resolved at runtime via `getWatchlistSettings` to honor edits — the daemon won't reuse stale settings if you ever change them via direct DB access).

---

## What can be edited later

Only the listed fields are mutable via `PUT /api/v1/watchlists/{id}`. Changing `spotify_url`, `settings` or `output_dir` requires deleting and recreating the watchlist.

| Field | Type |
|-------|------|
| `interval_hours` | int (must be > 0 to apply) |
| `sync_deletions` | bool |

---

## Sync deletions

When `sync_deletions: true`, tracks present in `TrackIDs` but no longer in the Spotify playlist are deleted from the local `downloadPath`.

**Multi-playlist protection.** Before deleting a file, SpotiFLAC checks whether the same `spotify_id` appears in **any other watchlist** (any other watchlist's `TrackIDs`). If yes, the file is preserved and stays in this watchlist's `TrackIDs` so it isn't re-queued. This works regardless of the actual on-disk path.

After deletion:

1. The `FilePath` on the corresponding job is cleared in BoltDB (so the track gets re-downloaded if it ever comes back).
2. The parent directory is recursively removed if empty (`removeEmptyParents`), stopping at `downloadPath`.

> Watchlists that share the **same `downloadPath`** but use different filename/folder templates can lead to weird edge cases (different files written for the same `spotify_id`). Use a dedicated `downloadPath` per watchlist when you can.

---

## Sync behaviour summary

| Event | What happens |
|-------|--------------|
| New tracks in Spotify | Queued via `EnqueueBatch` immediately. |
| Tracks removed from Spotify (with `sync_deletions: false`) | Kept in `TrackIDs`, file untouched. |
| Tracks removed from Spotify (with `sync_deletions: true`) | Deleted unless protected by another watchlist. |
| Failed jobs (daemon sync) | Stay in the failed bucket. **Not** auto-retried. |
| Failed jobs (manual sync) | Re-queued with the watchlist's **current** settings via `RequeueFailedJobs`. |
| Local file deleted manually | `recoverMissingFiles` removes the `track_id` on next sync → re-download. |
| Spotify playlist renamed | New name is detected; the old M3U8 file is deleted before the new one is written. |
| Watchlist deleted | M3U8 always deleted. Audio files deleted only if `sync_deletions` was on, and only if not referenced elsewhere. |

---

## Stats

`GET /api/v1/watchlists/{id}/stats`

```json
{
  "watchlist_id": "watch-1234567890",
  "total_tracks": 50,
  "downloaded":   47,
  "skipped":      1,
  "failed":       1,
  "pending":      1,
  "total_size_mb": 1234.5
}
```

| Field | Meaning |
|-------|---------|
| `total_tracks` | `len(pl.TrackIDs)` — current Spotify tracks for this watchlist |
| `downloaded` | Jobs in `done` state that belong to a track still in `TrackIDs` |
| `skipped` | Jobs in `skipped` state belonging to `TrackIDs`, **plus** tracks with no job (likely cleaned up by `CleanupOldJobs`) |
| `failed` | Jobs in `failed` state |
| `pending` | Jobs in `pending` or `downloading` state |
| `total_size_mb` | Sum of `total_size` across `done` and `skipped` jobs |

> A track without a job is counted as `skipped`. This is why `total_tracks` may equal `downloaded + skipped + failed + pending` exactly — orphan IDs are absorbed by `skipped`.

---

## History

`GET /api/v1/watchlists/{id}/history`

The history is **per-track** (newest first), built from every job ever enqueued for this watchlist:

```json
[
  {
    "track_name":  "Come Together",
    "artist_name": "The Beatles",
    "album_name":  "Abbey Road",
    "status":      "done",
    "total_size":  28.4,
    "updated_at":  1742334400,
    "file_path":   "/home/nonroot/Music/...",
    "error":       ""
  }
]
```

| Field | Meaning |
|-------|---------|
| `status` | `pending` · `downloading` · `done` · `failed` · `skipped` |
| `total_size` | MB |
| `updated_at` | Unix seconds |
| `file_path` | Absolute path on disk (empty if the file was deleted by `sync_deletions`) |
| `error` | Last error message, only populated for `failed` jobs |

The aggregated **per-sync** log is embedded inside the watchlist itself (the `sync_logs` array on `WatchedPlaylist`), capped at the last 20 entries:

```json
{
  "time":        "2026-03-30T10:00:00Z",
  "batch_id":    "watch-...-...",
  "new_tracks":  3,
  "downloaded":  3,
  "skipped":     44,
  "failed":      0,
  "deleted":     0
}
```

`Downloaded`, `Skipped`, `Failed` are filled when the batch completes (`OnBatchComplete`). `NewTracks` and `Deleted` are written immediately during the sync.

---

## Manual sync

Trigger an immediate sync from the UI (Watchlists → Sync button) or via API:

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists/{id}/sync \
  -H "Authorization: Bearer <token>"
```

Returns `202`. Manual sync runs the same `syncPlaylist` flow as the daemon **plus** retries failed jobs (`RequeueFailedJobs`) with the watchlist's current settings — useful when a previous sync failed due to a transient proxy outage and you want to retry now without waiting for the next interval.

---

## Removing a watchlist

`DELETE /api/v1/watchlists/{id}` returns `204`. Behaviour:

- The watchlist entry is removed from BoltDB.
- The M3U8 file is deleted (if `createM3u8File` was on). Empty `Playlists/` directory is removed.
- If `sync_deletions: true` was set on this watchlist, audio files are deleted (with multi-playlist protection — files referenced by another watchlist are preserved).

> Removing a watchlist does **not** cancel jobs already in flight; they finish as normal.

---

## M3U8 & Jellyfin integration

To generate M3U8 files that Jellyfin can read:

1. Enable `createM3u8File: true` in your settings.
2. Set `jellyfinMusicPath` to the path Jellyfin uses to mount your music library (e.g. `/media/music` if Jellyfin mounts the same folder under that path).
3. Make sure your watchlist's `downloadPath` is a child of the host folder Jellyfin sees.

Each watchlist gets its own `<downloadPath>/Playlists/<sanitized-name>.m3u8`. Inside the file, every entry has its `downloadPath` prefix replaced by `jellyfinMusicPath`.

The M3U8 is regenerated **after every sync** when no new tracks were enqueued, and again when each batch completes (so during a large initial sync, the file gradually fills as tracks finish — no waiting for the entire batch).

---

## Tips

- **Keep intervals ≥ 6 h.** Spotify's metadata API is rate-limited; very short intervals can cause failures.
- **Use a dedicated `downloadPath` per watchlist** to make `sync_deletions` predictable.
- **Failed tracks are retried automatically — but only on manual sync.** If you want every sync to retry failed tracks, hit the Sync button in the UI; the daemon does not.
- **The watcher honors live setting edits.** When `processJob` runs for a watchlist job, it pulls the watchlist's current `Settings` instead of using the snapshot taken when the job was created. This way, fixing a bad `folderTemplate` retroactively fixes pending jobs.
