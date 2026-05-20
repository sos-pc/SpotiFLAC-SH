# SpotiFLAC Web

[![Latest Release](https://img.shields.io/github/v/release/methammer/SpotiFLAC?style=flat-square)](https://github.com/methammer/SpotiFLAC/releases/latest)
[![Build](https://img.shields.io/github/actions/workflow/status/methammer/SpotiFLAC/docker.yml?style=flat-square)](https://github.com/methammer/SpotiFLAC/actions/workflows/docker.yml)
[![Docker Image](https://img.shields.io/badge/ghcr.io-methammer%2Fspotiflac-blue?style=flat-square&logo=docker)](https://github.com/methammer/SpotiFLAC/pkgs/container/spotiflac)

A self-hosted web app to download Spotify tracks in true FLAC from Tidal, Qobuz, Amazon Music & Deezer — no account required.

> **Based on [SpotiFLAC](https://github.com/afkarxyz/SpotiFLAC) by afkarxyz** — rewritten as a web server with multi-user support and Jellyfin integration.

## Features

- Download Spotify tracks, albums, playlists and artists as FLAC
- **Multi-user** — authentication via your Jellyfin server
- **Watchlists** — auto-sync Spotify playlists at configurable intervals
- **Smart sync** — detects new tracks, retries failed ones, optionally deletes removed tracks (with multi-playlist protection)
- **Jellyfin integration** — generates M3U8 playlist files automatically per user settings
- Real-time download queue (Server-Sent Events) with progress, speed and size
- **LAN bypass** — optional auto-login on local network (no password required)
- File browser, audio converter, audio analysis
- **Optional Tidal Premium** — OAuth 2.0 Device Code Flow for full FLAC; falls back to community HiFi proxies (preview-only as of May 2026) without any account
- **Auto proxy discovery** — Tidal proxy list refreshed every 6h from `tidal-uptime.geeked.wtf`
- Automatic BoltDB cleanup (deduplication every 24h)
- Docker-first deployment with GitHub Actions CI/CD

## Documentation

| Guide | Description |
|-------|-------------|
| [API Reference](docs/api-reference.md) | All REST endpoints with examples |
| [Authentication](docs/authentication.md) | JWT, API keys, LAN bypass |
| [Deployment](docs/deployment.md) | Docker, reverse proxy, env vars |
| [Settings Reference](docs/settings-reference.md) | All configurable options (camelCase keys) |
| [Watchlists](docs/watchlist.md) | Auto-sync playlists |
| [Tidal Auth](docs/tidal-auth.md) | Device Code Flow setup for Premium accounts |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and fixes |
| [External APIs](EXTERNAL_APIS.md) | Catalog of every external service used |
| [Credits](CREDITS.md) | Attributions for community projects, libraries, and proxies |

## Quick Start

### 1. Prerequisites

- Docker + Docker Compose
- A running [Jellyfin](https://jellyfin.org) instance (used for authentication)
- FFmpeg (bundled in the Docker image)

### 2. Deploy

```bash
git clone https://github.com/methammer/SpotiFLAC
cd SpotiFLAC
cp docker-compose.example.yaml docker-compose.yaml
# Edit docker-compose.yaml with your paths and settings
docker compose up -d
```

### 3. Configure `docker-compose.yaml`

```yaml
services:
  spotiflac:
    image: ghcr.io/methammer/spotiflac:latest
    container_name: spotiflac
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "6890:6890"
    environment:
      - JELLYFIN_URL=http://your-jellyfin-host:8096
      - JWT_SECRET=change-me-to-a-random-32-char-string
      # Optional: auto-login for direct LAN access (see below)
      # - DISABLE_AUTH_ON_LAN=true
    volumes:
      - /path/to/music:/home/nonroot/Music
      - /path/to/config:/home/nonroot/.SpotiFLAC
```

### 4. Access

Open `http://your-server:6890` and log in with your Jellyfin credentials.

> All Jellyfin users can log in. Each user has their own watchlists, download queue and settings.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `JELLYFIN_URL` | `http://localhost:8096` | URL of your Jellyfin instance, reachable from inside the container |
| `JWT_SECRET` | *(auto-generated)* | Secret for JWT signing. If unset, SpotiFLAC generates one and persists it in `<config>/jwt_secret` (mode `0600`). Set this env var to share a secret across replicas. |
| `DISABLE_AUTH_ON_LAN` | `false` | Auto-login as admin on direct LAN/localhost access (see below) |

## LAN Bypass (`DISABLE_AUTH_ON_LAN`)

When set to `true`, requests arriving **directly** on the local network (no reverse proxy) are automatically authenticated as a local admin — no Jellyfin login required.

**Security model:**
- Only `RemoteAddr` is trusted. If `X-Forwarded-For` or `X-Real-IP` is present, the bypass is refused — even if `RemoteAddr` is private.
- This means: direct LAN → bypass; via reverse proxy → normal Jellyfin login.
- Trusted ranges: loopback (`127.0.0.0/8`, `::1`), `10.0.0.0/8`, `172.16.0.0/12` (covers Docker bridge), `192.168.0.0/16`.
- **Requires port 6890 to be closed on the public internet.** If the port is exposed and `DISABLE_AUTH_ON_LAN=true`, anyone hitting the LAN-routed path becomes admin.

| Access path | Result |
|-------------|--------|
| `localhost:6890` / LAN direct | Auto-login as Local Admin |
| Via reverse proxy (internet) | Jellyfin login required |
| Internet direct (port open) | Would bypass auth — keep port closed |

```bash
# Verify the port is not exposed publicly before enabling
curl -m 5 http://$(curl -s ifconfig.me):6890/api/v1/auth/local -X POST
# Should timeout — if it responds, do NOT enable DISABLE_AUTH_ON_LAN
```

## Reverse Proxy (Nginx / SWAG example)

```nginx
location / {
    proxy_pass http://localhost:6890;
    proxy_http_version 1.1;

    # Required for SSE (download queue stream)
    proxy_set_header Connection '';
    proxy_buffering off;
    proxy_cache off;

    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 0;
}
```

> The `X-Forwarded-For` header set by the proxy is what prevents the LAN bypass from triggering on internet requests — never strip it.

## Watchlists

Watchlists track Spotify playlists and automatically sync them at a configurable interval.

- New tracks added to the Spotify playlist are downloaded automatically.
- Failed tracks are retried on **manual** sync (`SyncWatchlist` button / `POST /watchlists/{id}/sync`); the scheduled daemon does not auto-retry to avoid hammering rate-limited proxies.
- M3U8 files are regenerated for Jellyfin after each sync.
- Stats track total / downloaded / skipped / failed / pending per playlist.
- Playlist names are resolved from Spotify metadata on first sync and re-validated on each sync (renaming the Spotify playlist deletes the old M3U8 and creates a new one).

See [docs/watchlist.md](docs/watchlist.md) for details.

## Tidal Authentication

By default SpotiFLAC uses **community HiFi API proxies** — no Tidal account required. As of May 2026, those proxies are reachable but Tidal restricts the unauthenticated API to `assetPresentation: "PREVIEW"` (30-second segments). Full FLAC requires a personal token.

To get full FLAC, authenticate with a **Premium Tidal account** via the **OAuth 2.0 Device Code Flow** (same flow used by `tiddl`, `orpheusdl-tidal`, etc.).

**Via the UI (easiest):** Settings → Tidal Account → Connect with Tidal → open the displayed link → confirm in your Tidal account → SpotiFLAC detects authorization automatically (polls every 5 s).

**Automated script:**
```bash
python3 auth_tidal.py --host http://your-server:6890 --token <your-jwt-or-api-key>
```

**Manual (curl):**
```bash
# 1. Start
curl -s -X POST http://your-server:6890/api/v1/auth/tidal/device/start \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" -d '{}'

# Response gives device_code, user_code, verification_uri_complete, expires_in, interval

# 2. Open verification_uri_complete in a browser, log in with Tidal Premium, confirm

# 3. Poll every `interval` seconds until status=authorized
curl -s -X POST http://your-server:6890/api/v1/auth/tidal/device/poll \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"device_code":"<from step 1>"}'
```

- Token cached in `<config>/tidal_token.json` and **auto-refreshed** before expiry (5-minute window).
- If the refresh fails (subscription lapsed, token revoked) the file is deleted and SpotiFLAC falls back to community proxies.
- See [`docs/tidal-auth.md`](docs/tidal-auth.md) for the full walkthrough.

## Architecture

```
Browser → /api/v1/auth/login  → Jellyfin auth → JWT (24h, HMAC-SHA256)
Browser → /api/v1/auth/local  → LAN bypass    → JWT (admin, if DISABLE_AUTH_ON_LAN=true and request is LAN-direct)
Browser → /api/v1/* + JWT     → handlers (per-user filtered)
                              → BoltDB (jobs, watchlists, history, users, settings, api_keys, api_proxies, proxy_discovery)
                              → JobManager (1 worker, unified queue: manual + watchlist downloads)
                                → Tidal  (Device Code token → Community HiFi proxies, fallback loop)
                                → Qobuz  (musicdl.me primary → community proxies, fallback loop)
                                → Amazon (community proxy with X-Debug-Key)
                                → Deezer (community proxy /dl/ endpoint)

Background goroutines (started in main.go):
  - Watcher.daemon            — checks watchlists every 5 minutes
  - JobManager.cleanupLoop    — dedup BoltDB every 24h (after 5 min warm-up)
  - startProxyDiscovery       — refreshes Tidal proxy list every 6h from tidal-uptime.geeked.wtf
```

**Data isolation per user:**
- Watchlists & sync logs
- Download queue & download/fetch history
- Settings (quality, download path, filename templates, theme)

## Building from Source

```bash
# Requirements: Go 1.26+, Bun (frontend bundler)

# 1. Build the frontend (required: Go embeds frontend/dist via go:embed)
cd frontend
bun install --frozen-lockfile
bun run build
cd ..

# 2. Build the Go binary
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o spotiflac .

./spotiflac
```

```bash
# Or with Docker (multi-stage: bun → go → debian-slim)
docker build -t spotiflac:local .
```

## Data Storage

All data is stored in the config volume (`/home/nonroot/.SpotiFLAC`):

| File | Description |
|------|-------------|
| `jobs.db` | BoltDB — download jobs, watchlists, users, settings, history, api keys, proxies, discovery cache |
| `jwt_secret` | Auto-generated JWT signing key (mode 0600). Skipped when `JWT_SECRET` env var is set. |
| `tidal_token.json` | Cached Tidal Device Code token (mode 0644). Created on successful auth, deleted on disconnect or refresh failure. |
| `config.json` | Global settings fallback (legacy — kept so handlers can fall back to it when a user has no per-user settings yet). |

> **Backup:** a single `cp jobs.db jobs.db.bak` snapshots the entire application state.

## Differences from original SpotiFLAC

| Feature | Original | Web |
|---------|----------|-----|
| Interface | Desktop (Wails) | Web browser |
| Auth | None | Jellyfin login (+ API keys) |
| Multi-user | No | Yes |
| Watchlists + auto-sync | No | Yes |
| M3U8 Jellyfin | No | Yes |
| LAN bypass | No | Yes |
| Docker | No | Yes |
| Self-hosted | No | Yes |
| Real-time progress | Polling | Server-Sent Events |

## Changelog

### Unreleased — 2026-05
- **feat(discovery):** Level 1 proxy auto-discovery — background goroutine fetches `tidal-uptime.geeked.wtf` every 6h, merges results with user config (`GetTidalProxiesEffective`), persists last result in BoltDB so the effective list is correct immediately after restart. New fields exposed by `GET /apis/proxies`: `tidal_discovered`, `discovery_checked_at`, `discovery_source`.
- **chore(proxies):** Default Tidal list refreshed (May 2026): `eu-central.monochrome.tf`, `us-west.monochrome.tf`, `hifi-api.kennyy.com.br`, `api.monochrome.tf`, `monochrome-api.samidy.com`. All return `assetPresentation: "PREVIEW"` without a Premium token (full FLAC requires Tidal auth via Settings → Tidal Account).
- **chore(proxies):** Amazon endpoint moved from `amzn.afkarxyz.fun` → `amazon.spotbye.qzz.io`.
- **chore(proxies):** Qobuz primary moved to `musicdl.me` (POST + `X-Debug-Key`); legacy GET providers (`dab.yeet.su`, `dabmusic.xyz`, `qbz.afkarxyz.qzz.io`) all unreachable — empty default list.
- **fix(tidal):** Device Code Flow client_id moved to `4N3n6Q1x95LL5K7p` (sourced from `orpheusdl-tidal`). The previous TV `client_id` conflicted with the Tidal desktop app and forced the desktop logout.

### v3.4.0 — 2026-04-06
- **refactor(queue):** Unified progress tracking — removed dual queue system (`util/progress.go` global state eliminated); BoltDB + SSE is now the single source of truth for all download state.
- **feat(queue):** Live download speed transmitted via SSE and displayed in the Download Queue UI (was always "—").
- **fix(queue):** Clear History / Reset Queue now correctly updates the queue UI without requiring a page refresh.
- **fix(watchlist):** Pending track count now visible on watchlist cards.
- **fix(watchlist):** Sync log no longer shows "no changes" and "skipped" simultaneously.
- **fix(queue):** Session start time calculated from active jobs instead of being hardcoded to 0.
- **refactor(backend):** `ProgressWriter` reduced to a pure `io.Writer` wrapper with optional `SpeedCallback`; all global queue state removed.
- **refactor(frontend):** Removed dead code — `useDownloadProgress` hook (200 ms polling) and legacy `/jobs/legacy/*` API calls deleted.

### v3.3.0 — 2026-03-30
- **feat(proxies):** Amazon and Deezer use multi-proxy lists with automatic fallback (same pattern as Tidal/Qobuz).
- **feat(deezer):** `DownloadFromDeezmate` restored with full fallback loop and metadata embedding.
- **feat(ui):** Proxy configuration UI shows all 4 services as editable lists.

### v3.2.0 — 2026-03-29
- **feat(ui):** API Keys tab in Settings — create / list / revoke personal API keys (`sk_spotiflac_` prefix).
- **feat(ui):** Tidal Account tab in Settings — Device Code connect / disconnect flow.
- **feat(ui):** APIs tab — external service health dashboard + configurable proxy lists.
- **feat(security):** Admin gating — File Manager hidden from non-admin users.
- **feat(security):** Rate limiting on `POST /api/v1/auth/login` (10 attempts / 1 min window, 5-minute block on overflow).
- **feat(ui):** Login page shows distinct warning on `429`.

### v3.1.0 — 2026-03-28
- **feat(api):** Tidal auth routes migrated to `/api/v1/auth/tidal/*`.
- **feat(api):** New endpoints: `GET /api/v1/auth/tidal/status`, `DELETE /api/v1/auth/tidal`.
- **feat(api):** `GET/PUT /api/v1/apis/proxies` — BoltDB-backed proxy configuration, applied immediately without restart.
- **feat(api):** `GET /api/v1/apis/status` — parallel health check of all external services (30 s cache).
- **feat(api):** `GET/POST/DELETE /api/v1/auth/keys` — personal API key management.
- **feat(download):** Download queue switches to SSE (`/api/v1/jobs/stream`), replaces 500 ms polling.

### v3.0.x — 2026-03-24 → 03-26
- **feat(tidal):** Device Code Flow — Premium accounts authenticate via `/api/v1/auth/tidal/device/start` + `/device/poll` (replaces the older PKCE Web OIDC flow that was broken when Tidal dropped the `playback` scope from non-PKCE clients).
- **feat(tidal):** Community HiFi public instances added as automatic fallback when no token is present.
- **feat(tidal):** Public token used for search/ISRC resolution to avoid `403` on unauthenticated calls.
- **feat(tidal):** Automatic token refresh before expiry.
- **feat:** Unified download architecture — manual "Download FLAC" enqueues via JobManager (same queue, retry, progress as watchlist downloads); ~400 lines of redundant code removed.
- **fix(jobs):** JobManager infinite loop / false instant success.

### v2.x — 2026-03-22
- **feat:** Direct Tidal API authentication replacing proxy servers (Device Flow token).
- **feat:** Manual downloads resilient to Song.link outages (HTML scraping fallback + direct Tidal name/ISRC search).
- **fix:** Multiple Tidal `400`/`403` scope errors (`r_usr`, encoding); split search/download HTTP clients.

### v1.x → v1.3.x
- **feat:** Deezer ISRC fallback + direct Tidal search by name when Song.link unavailable.
- **feat:** Community API pool expanded; Song.link `__NEXT_DATA__` HTML scraping fallback.
- **fix:** Deezer disabled (domain expiry) — re-enabled in v3.3.0.
- **feat:** `DISABLE_AUTH_ON_LAN` — auto-login on direct LAN/localhost access.
- **fix:** Spotify URLs with `intl-fr/` prefix and `?si=` parameter now work for albums and artists.
- **fix:** Playlist names resolved from Spotify `playlist_info.owner.name` (the underlying API returns the playlist name in this field, not in `playlist_info.name`).
- **fix:** Watchlist stats — `missing = total - downloaded` (was incorrectly showing 100 % failed).
- **fix:** Race conditions on `TrackIDs` + `saveWatchlist`; `cleanupTicker` properly consumed; M3U8 ordering uses `sort.Slice`; `EnqueueBatch` deduplication.
- **fix:** Idempotent `CloseJobManager` (`sync.Once`); context-aware `songLinkSem`; key-by-key `ClearAllJobs` (no bucket drop); `recoverPendingJobs` resets progress to 0.
- **fix:** Graceful shutdown — `os.Exit` replaced by `SIGTERM`, proper `app.shutdown(ctx)` with timeout.
- **fix:** History DB merged into `jobs.db` — eliminates BoltDB lock conflicts on Docker restart.
- **fix:** `generateM3U8` reads per-user settings from BoltDB (not global `config.json`).
- **fix:** Auth guard on all RPC pollers — eliminates `401` flood on page load (fail2ban safe).
- **fix:** All history handlers pass `userID` from JWT (not from request body); admin-only `CleanupOldJobs`; path traversal protection on file upload.

## Disclaimer

This project is for **educational and private use only**.

**SpotiFLAC Web** is not affiliated with Spotify, Tidal, Qobuz, Amazon Music, Deezer, Jellyfin or any other service. You are solely responsible for ensuring your use complies with your local laws and the Terms of Service of the respective platforms.

## Credits

See [CREDITS.md](CREDITS.md) for the full list of community projects, libraries and proxy maintainers.

- [afkarxyz/SpotiFLAC](https://github.com/afkarxyz/SpotiFLAC) — original project
- [orpheusdl-tidal](https://github.com/Dniel97/orpheusdl-tidal) — Tidal Device Code credentials
- [tidal-uptime.geeked.wtf](https://tidal-uptime.geeked.wtf) — Tidal proxy auto-discovery feed
- [MusicBrainz](https://musicbrainz.org) · [LRCLIB](https://lrclib.net) · [Song.link](https://song.link) · [hifi-api](https://github.com/binimum/hifi-api)
