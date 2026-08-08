# Troubleshooting

---

## Login / Auth

### "Invalid credentials" on a correct Jellyfin password

- Verify `JELLYFIN_URL` is reachable **from inside the container's network**. The image runs `FROM scratch` (no shell, no `wget`/`curl`, nothing but the app itself — see [deployment.md](deployment.md#building-from-source)), so `docker exec spotiflac ...` won't work for this. Instead, run a throwaway container sharing the app's network namespace:
  ```bash
  docker run --rm --network container:spotiflac curlimages/curl -sv $JELLYFIN_URL/health
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

Should no longer happen through this app: the community proxies that served
previews were removed on 2026-07-28, precisely because that was all they could
serve without a personal token.

If you still see one, it came through the engine. Authenticate with a Premium
account to get the native full-FLAC path: **Settings → Tidal Account → Connect
with Tidal**. See [tidal-auth.md](tidal-auth.md).

### No Tidal link found

Song.link and its rate limit are gone (2026-07-28) — nothing calls it. Resolution
for a **native Tidal** download is now two steps, in `resolveTrackISRC`:

1. `GetISRCDirect` — Spotify's own catalog record for that exact track, cached.
2. `GetDeezerSearchFallback` — Deezer's public search, by name. A name match, so
   it can land on the wrong remaster; it only runs when step 1 came up empty.

The ISRC then reaches Tidal's own API through `GetTidalIDFromISRC`, and
`SearchTidalByName` is the last resort. If all fail the job is marked failed —
usually meaning the track genuinely isn't on Tidal under that name.

Providers other than Tidal never enter this path: the engine is handed the
Spotify URL and resolves internally.

### Downloaded file is 0 bytes or corrupt

- Usually an upstream issue: the source returned an HTML error page instead of audio. Retry the download.
- Check the APIs status dashboard, and the engine's own logs (`docker compose logs spotiflac-engine`) for the provider that failed.

### Tidal fails with "a personal Tidal token is required"

The native Tidal path needs a token, and there is no tokenless native fallback
any more — the community proxies that filled that role served previews only, and
the download path refuses previews, so the fallback could not succeed. Options:

1. **Authenticate** with your own Tidal Premium account (see [tidal-auth.md](tidal-auth.md)) — the only path to full FLAC natively.
2. **Add `tidal` to `ENGINE_SERVICES`** so tokenless Tidal is routed to the engine, which reaches it another way.
3. Switch to a provider the engine already handles (Qobuz, Amazon, Deezer).

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

### M3U8 file references broken paths after I moved files

The M3U8 generator reads the `SPOTIFY_ID` tag from each audio file under `downloadPath`, so a file moved within `downloadPath` should still resolve correctly on the next sync.

If you see broken paths:

1. Check the file actually has the tag: `ffprobe -v error -show_entries format_tags=SPOTIFY_ID -of compact <file>`.
2. If the tag is missing (legacy file from a build before May 2026), run `POST /api/v1/admin/retag-legacy` once to back-fill it.
3. If the file is **outside** `downloadPath`, move it back inside or update `downloadPath` in the watchlist's settings.

### `sync_deletions` deleted a file it shouldn't have

- Multi-playlist protection only matches by `spotify_id` against **other watchlists' `track_ids`**, not the on-disk file. If two watchlists happened to write the same `spotify_id` to different paths (different folder/filename templates), the protection still works for the catalog, but the on-disk file mapping is up to you to keep coherent.
- Make sure both watchlists actually contain the track at sync time. A track removed simultaneously from both will still be deleted.

---

## FFmpeg

### "ffmpeg not found" error

> ⚠️ **Known bug in Docker deployments since 2026-07-12 — this section used to say it "should never
> happen". It does.** The bundled FFmpeg/FFprobe binaries are present in the image but cannot
> execute in the `scratch` runtime: they are dynamically linked (against glibc/libstdc++, verified
> on the pinned asset) and `scratch` ships no ELF loader. `exec` then fails with
> `no such file or directory` **naming a file that exists** — that error reports the missing
> *interpreter*, not the missing binary. Diagnosis, blast radius and fix options:
> [ffmpeg-runtime-regression.md](archive/ffmpeg-runtime-regression.md).

The intent (not currently achieved — see above): an FFmpeg/FFprobe build is fetched and verified against a checksum in the Dockerfile's build stage, then copied straight into the (shell-less) runtime image at `/usr/local/bin/ffmpeg` / `/usr/local/bin/ffprobe`.

- If running from source on bare metal: install FFmpeg system-wide (`apt install ffmpeg`, `brew install ffmpeg`, etc.). The web build does not bundle a fallback installer; the Wails desktop build did, but those code paths are unused here.
- Verify SpotiFLAC sees it: `GET /api/v1/system/ffmpeg` should return `{"installed": true, "ffprobe_installed": true, …}`. **If either flag is `false` on a Docker deployment, you are hitting the bug above** — the flags are computed by actually running `ffmpeg -version` / `ffprobe -version` (`backend/audio/ffmpeg.go`), so they go `false` exactly when the binaries can't exec.

### FFmpeg decryption fails (Amazon Music)

Amazon tracks are delivered as encrypted `.m4a` and decrypted via `ffmpeg -decryption_key`. If decryption fails:

- Make sure FFmpeg version ≥ 4.4 — the Docker image bundles a recent BtbN/FFmpeg-Builds static release (well past that), so this only matters if you're running from source with your own system FFmpeg.
- The job's `error` field contains the tail of the FFmpeg output — it usually pinpoints the issue (network error during fetch, missing key, malformed stream).

---

## Performance

### Downloads are slow / sequential

By design, SpotiFLAC processes **one download at a time** (`jobWorkers = 1` in `jobs_worker.go`). This avoids hammering upstream sources and getting IP-banned. For large playlists (100+ tracks), expect a long initial run; the M3U8 file is updated incrementally as tracks finish.

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

The container runs as numeric **uid/gid 1000** (`USER 1000:1000` in the Dockerfile — the runtime image is `FROM scratch`, which has no `/etc/passwd` to resolve a named user against, so there's no `useradd`/shell involved at all).

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

## Download engine

### `qobuz is only available through the download engine`

Exactly what it says: since v4.0.0 Qobuz, Amazon and Deezer have no native Go path. Set both `ENGINE_URL` and `ENGINE_SERVICES` on the app container, and make sure the engine service is actually running. See [deployment.md](deployment.md).

### Which build am I actually running?

`docker compose pull` **without** `up -d` leaves the previous container running. The engine says what it is at startup and on `/health`:

```bash
docker compose logs spotiflac-engine | grep "engine shim"
#   engine shim: SpotiFLAC 1.6.0, image d0b573a
```

`docker inspect spotiflac-engine --format '{{index .Config.Labels "org.opencontainers.image.revision"}}'` answers the same question from the outside.

### `Browser failed to start within timeout` / `No usable sandbox!`

The engine drives Chromium for several provider routes. Both symptoms mean it could not start one:

- **`No usable sandbox!`** — `cap_drop: ALL` blocks the user-namespace sandbox and `no-new-privileges` blocks the setuid helper. Our image ships `/usr/bin/chromium` as a wrapper that passes `--no-sandbox`; if you see this, you are on an image older than `512bc13`.
- **A crash with no message** — missing `shm_size: 1g`. Chromium puts renderer shared memory in `/dev/shm` and Docker's 64 MB default is not enough. Check with `docker exec spotiflac-engine df -h /dev/shm`.

Test it directly — note the absence of `--headless`, which would bypass both the display and the sandbox and tell you nothing:

```bash
docker exec spotiflac-engine sh -c 'chromium https://example.com >/tmp/c.log 2>&1 & sleep 8; pgrep -c chromium.real; pkill -f chromium.real'
```

### `produced file is not readable audio`

The engine downloaded something that is not the audio its filename claims — usually a stream that died mid-transfer, leaving an HTML error body or a truncated file at a `.flac` path. **This is the shim refusing to call it a success**, not a bug. The chain moves on to the next provider.

Left unchecked the engine reports these as successful, so if you ever see a 0-byte or unplayable file reach the library, that is the interesting failure — see [module-engine.md](module-engine.md) §5.

### A provider that worked yesterday fails today

Its host probably died. The engine resolves its endpoints from an encrypted registry fetched **at runtime**, so upstream replaces dead hosts without publishing a release, and the same provider fails differently from one day to the next. Measured 2026-08-04: three of nine Qobuz hosts no longer resolve at all, and Deezer's primary route fails on every attempt with `ext:deezer` carrying the provider.

**Open Settings → APIs.** There is one row per delegated provider, named
`Qobuz · engine`, `Deezer · engine` and so on, and it answers the only question
worth asking first — is it us or is it them?

| what you see | what it means |
|---|---|
| provider rows red, `Engine` green | upstream's hosts are down. Nothing to fix here; the row's error carries the count and the reason, e.g. `0/1 reachable — HTTP 403` |
| `Engine` red | the sidecar itself is unreachable. Your deployment, not upstream — see *Container exits immediately on startup* above |
| all green but downloads still fail | not reachability. Read the engine log for the per-attempt reason |
| no provider rows at all | the engine predates the endpoint, or is still gathering its first sample. Reload in a minute |

A row reading `3/48 reachable` is worth noting even when green: the provider works
through a handful of surviving mirrors and is one outage away from red.

The rows come from the engine asking **itself** which endpoints it would try —
we do not maintain a list of provider hosts. The engine resolves them from an
encrypted registry fetched **at runtime**, which is why the same provider fails
differently from one day to the next without any release being published.

Measured 2026-08-07, with the app reporting `Engine: ok` throughout: Qobuz 3
reachable out of 48, Deezer 0 of 1 (`HTTP 403`), Amazon 0 of 1 (connection
refused), Tidal 2 of 2. Establishing that by hand took about an hour, which is
why the board now shows it.

To rule out container DNS specifically:

```bash
docker exec spotiflac-engine python -c "import socket; print(socket.gethostbyname('pypi.org'))"
```

If that resolves, container DNS is fine and the dead hosts are upstream's.

### Tidal returns `410 - The v1 download API has been retired`

Expected without a token, and not fixable from here — it is upstream's endpoint table. Tokenless Tidal survives through the `ext:tidal-web` extension route. Authenticate with a Tidal Premium account for the native full-FLAC path.

---

## Tags / Library

### Existing files don't have a `SPOTIFY_ID` tag

If you upgraded from a build that didn't embed `SPOTIFY_ID` automatically, run **once** as an admin:

```bash
curl -s -X POST http://spotiflac.example.com/api/v1/admin/retag-legacy \
  -H "Authorization: Bearer <admin-jwt-or-api-key>" | jq
```

The handler walks every Done/Skipped job in BoltDB whose file still exists on disk and writes the `SPOTIFY_ID` tag in place. Idempotent — safe to re-run.

### Files moved/renamed manually disappear from the M3U8

This shouldn't happen if the file still has its `SPOTIFY_ID` tag — `meta.BuildSpotifyIDIndex` walks the entire `downloadPath` recursively. Confirm the file is still under `downloadPath` and has the tag:

```bash
ffprobe -v error -show_entries format_tags=SPOTIFY_ID -of compact <file>
```

If the tag was stripped (e.g. by re-encoding), re-download the track via a manual sync of the watchlist.

---

## Debug logs

The server logs to stdout. In Docker, follow them with:

```bash
docker compose logs -f spotiflac
```

In the UI, the **Debug Logger** tab (last sidebar entry, admin-only) shows the same log lines without needing shell/`docker` access — useful on a managed host or when you can't reach the server's stdout. It loads an initial snapshot from `GET /api/v1/admin/logs` (last 1000 lines, in-memory) then tails new lines live over the same SSE connection as the download queue (`server_log` events on `GET /api/v1/jobs/stream`).
