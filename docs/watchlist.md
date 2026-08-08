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

### M3U8 generation: filesystem-driven via `SPOTIFY_ID`

`generateM3U8ForPlaylist` (in `watcher.go`) does not consult BoltDB to decide what files exist on disk. Instead:

1. It walks `<downloadPath>` recursively with `meta.BuildSpotifyIDIndex(downloadPath)`, reading the `SPOTIFY_ID` tag from every `.flac`, `.mp3` and `.m4a` file (FLAC and MP3 use native readers; M4A uses `ffprobe` via `util.ReadFFprobeTags`).
2. It produces a `map[SpotifyID]filePath` index.
3. For each Spotify ID in `pl.TrackIDs` (in order), it looks up the file in the index. If found, the path is added to the M3U8.
4. Tracks that don't have a `SPOTIFY_ID` tag (legacy files, downloaded before this feature) are recovered through a fallback that consults the BoltDB job records.
5. The M3U8 is written atomically (`os.Rename` from a `.tmp`).

**Practical implications:**

- Files moved/renamed manually are still recognised as long as their `SPOTIFY_ID` tag is intact.
- `CleanupOldJobs` (BoltDB dedup, every 24 h) no longer breaks playlists — even if a job is removed from BoltDB, the file's tag stays.
- After upgrading from a build that didn't embed `SPOTIFY_ID`, run `POST /api/v1/admin/retag-legacy` once to back-fill existing files (see [api-reference.md](api-reference.md#post-apiv1adminretag-legacy)).

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

The settings block follows the `JobSettings` shape (camelCase). It is **stored as-is on the watchlist** and used for every download triggered by this watchlist — `JobManager.getWatchlistSettings` (a lookup closure wired in `watcher.go`, not a settings copy baked into the job) re-reads the watchlist's current `Settings` when a job actually runs, so the daemon won't reuse stale settings if you ever change them via direct DB access.

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
| Local file moved/renamed manually | Still recognised by the M3U8 generator (via `SPOTIFY_ID` tag) — no action needed. |
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
| `downloaded` | Track has a `present` row in the SQLite catalog (`library_files`), **or** (catalog unavailable/no row yet) a job in `done` state |
| `skipped` | Job in `skipped` state, **or** (catalog disabled) a track with no job at all |
| `failed` | Job in `failed` state |
| `pending` | Job in `pending`/`downloading` state, **or** a track with no catalog row and no job (catalog enabled — it genuinely hasn't been downloaded yet) |
| `total_size_mb` | Catalog `file_size` for every `downloaded` track (bytes → MB), **plus** `total_size` from `done`/`skipped` jobs not yet reflected in the catalog |

> Each track in `TrackIDs` is resolved to exactly one bucket, so `total_tracks` always equals `downloaded + skipped + failed + pending`.
>
> **Why the catalog first:** BoltDB job rows are pruned by `CleanupOldJobs` every 24h, but the catalog's `library_files` row for a track survives indefinitely and holds its real on-disk `file_size` — a job's `total_size` can also go stale after a later quality-upgrade re-download landed at the same path. Before this, a playlist whose jobs had mostly aged out of BoltDB could report a plausible-looking `downloaded` count while `total_size_mb` was wildly understated, since size could only ever be summed from a job that still existed. If you're on a build before this fix, run `POST /api/v1/admin/library-rebuild` (or the watchlist's Repair button) once to backfill the catalog.

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

> `jobWorkers` is `1` — every download across every watchlist is serialized through one shared queue. A large batch can take far longer to drain than 20 sync cycles, so by the time `OnBatchComplete` fires, the entry it was going to fill in may already have scrolled out of the 20-entry cap. When that happens, a standalone entry carrying just the batch's counts is appended instead of silently dropping them — you may occasionally see two log lines for what was really one sync (one with `new_tracks`, a later one with `downloaded`/`skipped`/`failed`).

---

## Manual sync

Trigger an immediate sync from the UI (Watchlists → Sync button) or via API:

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists/{id}/sync \
  -H "Authorization: Bearer <token>"
```

Returns `202`. Manual sync runs the same `syncPlaylist` flow as the daemon **plus** retries failed jobs (`RequeueFailedJobs`) with the watchlist's current settings — useful when a previous sync failed due to a transient proxy outage and you want to retry now without waiting for the next interval.

---

## Freshness check

`GET /api/v1/watchlists/{id}/freshness` is a **read-only** "is this playlist up to date" check — it fetches the live Spotify playlist and compares it against local state without enqueuing anything, deleting anything, or mutating the watchlist. Safe to call as often as you like (e.g. before deciding whether to sync).

```bash
curl -s http://spotiflac.example.com/api/v1/watchlists/{id}/freshness \
  -H "Authorization: Bearer <token>"
```

Returns counts of tracks new on Spotify but not tracked locally, tracked locally but removed from Spotify, tracked but missing a resolvable file, and whether the M3U8 on disk has fewer entries than are actually resolvable right now (`m3u8_stale` — the signal to run Repair). Full response shape in [api-reference.md](api-reference.md#get-apiv1watchlistsidfreshness).

## Repair

`POST /api/v1/watchlists/{id}/repair` — the "fix this playlist" button. It's the per-watchlist, UI-reachable equivalent of the admin-only `retag-legacy` + `library-rebuild` maintenance endpoints: it retags this watchlist's own files that are missing their `SPOTIFY_ID` tag, rebuilds the catalog for this watchlist's own download path, then force-regenerates the M3U8 (bypassing the shrink-guard normal sync respects, since an explicit repair should reflect the true current state even if it's smaller than what's on disk right now).

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/watchlists/{id}/repair \
  -H "Authorization: Bearer <token>"
```

Returns `202` — the walk can take a while on a large library, so it runs in the background; completion arrives as a `watchlist_repaired` event on the same SSE stream as sync/download progress. Reach for this when `freshness` reports `m3u8_stale: true`, or after upgrading from a build that didn't embed `SPOTIFY_ID` tags yet.

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

Each watchlist gets its own `<downloadPath>/Playlists/<sanitized-name> [xxxxxxxx].m3u8`, where the bracketed part is eight hex digits derived from the watchlist ID. Inside the file, every entry has its `downloadPath` prefix replaced by `jellyfinMusicPath`.

The M3U8 is regenerated **after every sync** when no new tracks were enqueued, and again when each batch completes (so during a large initial sync, the file gradually fills as tracks finish — no waiting for the entire batch).

### Why the filename carries a code, and why it shows up in Jellyfin

The suffix disambiguates: two watchlists whose names collide once sanitized (`AC/DC Hits` and `AC:DC Hits` both become `AC DC Hits`) would otherwise write to the same file, and whichever synced last would silently overwrite the other on every cycle.

It is visible in Jellyfin because **Jellyfin names a playlist after the file, and has no other source.** The M3U8 format has a `#PLAYLIST:` field for exactly this, and Jellyfin ignores it — [an open feature request](https://features.jellyfin.org/posts/3104/support-playlist-field-in-m3u-playlists). Verified empirically on 2026-08-08: a file named `ZZTest [deadbeef].m3u8` containing `#PLAYLIST:Titre Propre Sans Code` appeared in Jellyfin as `ZZTest [deadbeef]`. So there is no metadata field to hide the code in — the only way to a clean name is a clean filename, which means handling collisions somewhere other than the filename.

### Albums do not get a playlist

A watchlist tracking a Spotify album writes no M3U8. Downloads land in `<Artist>/<Album>/`, which Jellyfin already indexes as an album, so a playlist file put the identical content in the Playlists tab a second time. Tracking, syncing and downloading are unaffected.

Artist watchlists **do** get one: an artist is a growing collection spanning many releases, which is the case where a flat playlist shows something the folder tree does not.

### Two producers write to `Playlists/`, and nothing records which

Worth knowing before you reason about these files:

- **watchlist syncs** — `GenerateM3U8ForPlaylist`, one file per watchlist
- **manual batches** — `OnManualBatchComplete`, one file per download started from the search bar that asked for a playlist

Both use the same `<name> [xxxxxxxx].m3u8` naming, and nothing on disk says which produced a given file. A file with no watchlist behind it is therefore **not** evidence of an orphan — it is the normal case for every manual download.

That cost a regression on 2026-08-08: a sweep that deleted "any suffixed file no watchlist owns" ran on the reference deployment and destroyed five legitimate manual-batch playlists within minutes of shipping. It has been removed. Orphan cleanup needs a record of what was written and by whom; until that exists there is no sweep.

### Orphans are prevented at the source instead

Removing or renaming a watchlist deletes its file, and **neither is gated on `createM3u8File`**. The setting governs whether files are written; a file already on disk has to be tidied up either way. Gating it is how one deployment ended up with a playlist that outlived its watchlist by 27 days — removed while the setting was off, with nothing left that knew about it.

A file orphaned some other way stays until you delete it by hand.

---

## Tips

- **Keep intervals ≥ 6 h.** Spotify's metadata API is rate-limited; very short intervals can cause failures.
- **Use a dedicated `downloadPath` per watchlist** to make `sync_deletions` predictable.
- **Failed tracks are retried automatically — but only on manual sync.** If you want every sync to retry failed tracks, hit the Sync button in the UI; the daemon does not.
- **The watcher honors live setting edits.** When `processJob` runs for a watchlist job, it pulls the watchlist's current `Settings` instead of using the snapshot taken when the job was created. This way, fixing a bad `folderTemplate` retroactively fixes pending jobs.
- **After upgrading**, run `POST /api/v1/admin/retag-legacy` once so existing files become resolvable by tag and can be picked up by the M3U8 generator.
