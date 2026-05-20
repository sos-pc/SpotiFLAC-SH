# Troubleshooting

---

## Login / Auth

### "Invalid credentials" on a correct Jellyfin password

- Verify `JELLYFIN_URL` is reachable **from inside the container**:
  ```bash
  docker exec spotiflac wget -qO- $JELLYFIN_URL/health
  # or
  docker exec spotiflac sh -c 'wget -qS -O- $JELLYFIN_URL 2>&1 | head'
  ```
- Inside Docker, `localhost` points to the container itself — never to the host. Use the host's LAN IP, the Docker bridge gateway (`172.17.0.1`), `host.docker.internal` (Docker Desktop), or put both services on a shared Docker network.
- If your Jellyfin is behind a reverse proxy too, make sure the URL you set is the **internal** one (no auth headers, no SSO).

### `429` on login

The rate limiter is **10 attempts per 1-minute window per source IP**, with a **5-minute block** on overflow. Successful logins do **not** reset the counter — the only way out of a block is to wait 5 minutes (`Retry-After: 300`).

- If you're behind a reverse proxy, make sure it forwards `X-Forwarded-For` (otherwise every internet user shares one bucket).
- If you accidentally locked yourself out and need to recover, restart the container — the rate limiter is in-memory only.

### Token expired loop / constant 401

- The JWT lifetime is 24 hours. If the host clock has drifted significantly, tokens may be rejected as expired or never accepted. Check `docker exec spotiflac date`.
- The frontend dispatches an `auth:expired` event on `401`; if you see this in a tight loop, you probably have an outdated tab still authenticated with an old `JWT_SECRET`. Clear `localStorage` and log in again.

---

## Downloads

### All downloads fail immediately

1. Open **Settings → APIs**. The status dashboard shows every external service (refreshed every 30 s). Anything red is the cause.
2. Click on a job in the queue — its `error` field has the proxy / HTTP error returned upstream.
3. Try switching `downloader` in Settings (`auto` → `qobuz` or `tidal`) to bypass a single broken provider.

### Tidal downloads start but produce a 30-second file (preview)

This is the "current state" of the community Tidal proxies (May 2026). Without a personal token, Tidal returns `assetPresentation: "PREVIEW"` to every community proxy.

- The status dashboard flags this as `ratelimited` with the message `PREVIEW only — full FLAC requires Tidal Premium token (Settings → Tidal Account)`.
- To get full FLAC, authenticate with a Premium account: **Settings → Tidal Account → Connect with Tidal**. See [tidal-auth.md](tidal-auth.md).

### "Song.link rate limited" / no Tidal/Qobuz link found

Song.link's free API is heavily rate-limited. SpotiFLAC has three layers of fallback (in `getStreamingURLs`):

1. Deezer public search (no rate limit) — fast, gives ISRC.
2. Apple Music scraping via iTunes Search → `song.link/i/<id>` (different quota from `/s/<spotifyID>`).
3. HTML scraping of `song.link/s/<spotifyID>` (`__NEXT_DATA__`).
4. Direct Tidal search by name (`tidal.NewTidalDownloader().SearchTidalByName`).

If all four fail, the job is marked failed. Wait a few minutes and retry — the in-memory rate-limit cache clears automatically.

### Downloaded file is 0 bytes or corrupt

- Almost always a CDN/proxy issue: the proxy returned an HTML error page instead of audio. Retry the download.
- Check the APIs status dashboard. If a specific proxy is consistently failing, remove it via **Settings → APIs → Proxy Configuration**.
- Submit a working alternative through the same UI; lists are applied immediately without restart.

### "All Tidal proxies failed"

Every entry in `GetTidalProxiesEffective()` (user config + auto-discovery) returned an error. Options:

1. Wait — the discovery goroutine refreshes the list every 6 h from `tidal-uptime.geeked.wtf`.
2. Force a refresh by restarting the container (`loadSavedDiscovery` runs at startup).
3. Authenticate with your own Tidal Premium account (see [tidal-auth.md](tidal-auth.md)) — that bypasses community proxies entirely.
4. Switch to Qobuz or Amazon (`downloader: qobuz`) in Settings.

To inspect what discovery returned:

```bash
curl -s http://spotiflac.example.com/api/v1/apis/proxies \
  -H "Authorization: Bearer <token>" | jq '{tidal_discovered, discovery_checked_at, discovery_source}'
```

### Track not available on any platform

Some tracks are not available in lossless on Tidal/Qobuz/Amazon/Deezer (regional restrictions, Spotify-exclusive content, etc.). SpotiFLAC can only download what the platforms have. The job will be marked `failed` with the most recent provider error.

---

## Watchlists

### Watchlist not syncing

- The daemon ticks every 5 minutes and only syncs watchlists whose `last_sync + interval_hours` is in the past. Trigger a manual sync to test immediately.
- Look at the watchlist's `sync_logs` (returned by `GET /watchlists`) for error messages. The most recent attempt is the last entry.

### Stats show numbers that don't add up

- `total_tracks = downloaded + skipped + failed + pending` exactly. Tracks present in `track_ids` but with no matching job (typically purged by the 24-h dedup loop) count as `skipped` so the equation always holds.
- If you moved or deleted files manually, `recoverMissingFiles` will catch them on the next sync and re-queue them. Refresh stats after the sync completes.

### `sync_deletions` deleted a file it shouldn't have

- Multi-playlist protection only matches by `spotify_id` against **other watchlists' `track_ids`**, not the on-disk file. If two watchlists happened to write the same `spotify_id` to different paths (different folder/filename templates), the protection still works for the catalog, but the on-disk file mapping is up to you to keep coherent.
- Make sure both watchlists actually contain the track at sync time. A track removed simultaneously from both will still be deleted.

---

## FFmpeg

### "ffmpeg not found" error

In Docker deployments this should never happen — `apt install ffmpeg` runs in stage 3 of the Dockerfile.

- If running from source on bare metal: install FFmpeg system-wide (`apt install ffmpeg`, `brew install ffmpeg`, etc.). The web build does not bundle a fallback installer; the Wails desktop build did, but those code paths are unused here.
- Verify SpotiFLAC sees it: `GET /api/v1/system/ffmpeg` returns `{"installed": true, "ffprobe_installed": true, "ffmpeg_path": "/usr/bin/ffmpeg"}`.

### FFmpeg decryption fails (Amazon Music)

Amazon tracks are delivered as encrypted `.m4a` and decrypted via `ffmpeg -decryption_key`. If decryption fails:

- Make sure FFmpeg version ≥ 4.4. The Dockerfile's `debian:bookworm-slim` base provides 5.x.
- The job's `error` field contains the tail of the FFmpeg output — it usually pinpoints the issue (network error during fetch, missing key, malformed stream).

---

## Performance

### Downloads are slow / sequential

By design, SpotiFLAC processes **one download at a time** (`jobWorkers = 1` in `jobs.go`). This avoids hammering community proxies and getting IP-banned. For large playlists (100+ tracks), expect a long initial run; the M3U8 file is updated incrementally as tracks finish.

### UI feels slow / SSE not connecting

The download queue uses Server-Sent Events (`/api/v1/jobs/stream`). If your reverse proxy buffers responses, the UI sees nothing.

- Nginx: `proxy_buffering off;` + `proxy_read_timeout 0;` (see [deployment.md](deployment.md))
- Caddy: `flush_interval -1`
- Cloudflare proxied (orange cloud): SSE works only on Pro+ plans. Use `:gray-cloud:` (DNS only) on free.

---

## Docker

### Permission denied on volume

```bash
# Fix ownership on the host (uid 1000 = nonroot in the image)
chown -R 1000:1000 /path/to/config /path/to/music
```

The container runs as `nonroot` with **uid 1000** (defined in the Dockerfile: `useradd -u 1000 -m -s /bin/bash nonroot`).

### Container exits immediately on startup

```bash
docker compose logs spotiflac
```

Common causes:

- `JELLYFIN_URL` cannot be parsed (missing scheme, malformed). Must be a valid HTTP URL like `http://jellyfin:8096`.
- Port 6890 already in use on the host. Change the port mapping in `docker-compose.yaml`.
- BoltDB lock — only one process can open `jobs.db` at a time. Make sure no orphan container is holding the volume:
  ```bash
  docker ps -a | grep spotiflac
  docker rm -f <leftover-id>
  ```

### Old container still running after update

```bash
docker compose down && docker compose pull && docker compose up -d
```

`docker compose pull` does not stop running containers automatically.

---

## Discovery / Proxies

### `tidal_discovered` is empty or stale

- The goroutine fetches `https://tidal-uptime.geeked.wtf` every 6 h, plus once at startup after a 0–30 s random jitter.
- Cached results older than 24 h are ignored on restart (`maxDiscoveryAge`). If you've been offline for days, you may briefly see no discovery data until the first run completes.
- If the upstream feed itself is down, `discovery_source` is still `"tidal-uptime.geeked.wtf"` but `error` will be set in the BoltDB record (currently not exposed via the API).

### Community proxies all show `ratelimited` with "PREVIEW only" error

This is **expected** as of May 2026 — see "Tidal downloads start but produce a 30-second file" above. Authenticate with a Tidal Premium account to unlock full FLAC.

---

## Debug logs

The server logs to stdout. In Docker, follow them with:

```bash
docker compose logs -f spotiflac
```

In the UI, the **Debug Logger** tab (last sidebar entry) shows a live stream of recent log lines and is useful when you can't reach the server's stdout.
