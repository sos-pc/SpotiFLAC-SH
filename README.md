# SpotiFLAC Web

[![Latest Release](https://img.shields.io/github/v/release/methammer/SpotiFLAC?style=flat-square)](https://github.com/methammer/SpotiFLAC/releases/latest)
[![Build](https://img.shields.io/github/actions/workflow/status/methammer/SpotiFLAC/docker.yml?style=flat-square)](https://github.com/methammer/SpotiFLAC/actions/workflows/docker.yml)
[![Docker Image](https://img.shields.io/badge/ghcr.io-methammer%2Fspotiflac-blue?style=flat-square&logo=docker)](https://github.com/methammer/SpotiFLAC/pkgs/container/spotiflac)

A self-hosted web app to download Spotify tracks in true FLAC from Tidal, Qobuz, Amazon Music & Deezer — no account required.

> **Based on [SpotiFLAC](https://github.com/afkarxyz/SpotiFLAC) by afkarxyz** — rewritten as a web server with multi-user support and Jellyfin integration.

## Features

- 🎵 Download Spotify tracks, albums, playlists and artists as FLAC
- 👥 **Multi-user** — authentication via your Jellyfin server
- 📋 **Watchlists** — auto-sync Spotify playlists at configurable intervals
- 🔁 **Smart sync** — detects new tracks, retries failed ones, optionally deletes removed tracks (with multi-playlist protection)
- 🎬 **Jellyfin integration** — generates M3U8 playlist files automatically per user settings
- 📊 Real-time download queue with progress, speed and size
- 🏠 **LAN bypass** — optional auto-login on local network (no password required)
- 🗂️ File browser, audio converter, audio analysis
- 🧹 Automatic BoltDB cleanup (deduplication every 24h)
- 🐳 Docker-first deployment with GitHub Actions CI/CD

## Screenshots

> *(add your screenshots here)*

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
| `JELLYFIN_URL` | `http://localhost:8096` | URL of your Jellyfin instance |
| `JWT_SECRET` | *(insecure default)* | Secret key for JWT signing — **change in production** |
| `DISABLE_AUTH_ON_LAN` | `false` | Auto-login as admin on direct LAN/localhost access (see below) |

## LAN Bypass (`DISABLE_AUTH_ON_LAN`)

When set to `true`, requests arriving **directly** on the local network (no reverse proxy) are automatically authenticated as a local admin — no Jellyfin login required.

**Security model:**
- Only `RemoteAddr` is trusted (no `X-Forwarded-For` / `X-Real-IP`)
- If a request comes through a reverse proxy (SWAG/Nginx), it carries `X-Forwarded-For` → normal Jellyfin login is enforced
- Applies to: loopback (`127.x`), LAN (`192.168.x`, `10.x`), Docker bridge (`172.16/12`)
- **Requires port 6890 to be closed on the internet** (not exposed publicly)

| Access path | Result |
|-------------|--------|
| `localhost:6890` / LAN direct | Auto-login as Local Admin ✅ |
| Via reverse proxy (internet) | Jellyfin login required ✅ |
| Internet direct (port open) | ⚠️ Would bypass auth — keep port closed |

```bash
# Verify the port is not exposed publicly before enabling
curl -m 5 http://$(curl -s ifconfig.me):6890/auth/local -X POST
# Should timeout — if it responds, do NOT enable DISABLE_AUTH_ON_LAN
```

## Reverse Proxy (Nginx / SWAG example)

```nginx
location / {
    proxy_pass http://localhost:6890;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection 'upgrade';
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 0;
}
```

> The `X-Forwarded-For` header set by the proxy is what prevents the LAN bypass from triggering on internet requests.

## Watchlists

Watchlists track Spotify playlists and automatically sync them at a configurable interval.

- New tracks added to the Spotify playlist are downloaded automatically
- Failed tracks are retried on the next sync
- M3U8 files are regenerated for Jellyfin after each sync
- Stats show total / downloaded / missing per playlist
- Playlist names are resolved from Spotify metadata on first sync

## Architecture

```
Browser → /auth/login     → Jellyfin auth → JWT (24h)
Browser → /auth/local     → LAN bypass    → JWT (admin, if DISABLE_AUTH_ON_LAN=true)
Browser → /api/rpc + JWT  → handlers (per-user filtered)
                          → BoltDB (jobs, watchlists, history, users)
                          → Download workers (3 parallel)
```

**Data isolation per user:**
- Watchlists & sync history
- Download queue & fetch history
- Settings (quality, download path, filename templates)

## Building from Source

```bash
# Requirements: Go 1.21+, Bun
cd frontend && bun install && bun run build
cd ..
go build -o spotiflac .

# Or with Docker
docker build -t spotiflac:local .
```

## Data Storage

All data is stored in the config volume (`/home/nonroot/.SpotiFLAC`):

| File | Description |
|------|-------------|
| `jobs.db` | Download jobs, watchlists, users, history (BoltDB — single file) |
| `config.json` | Global settings fallback |

> Since v1.1.7, download history is stored in `jobs.db` (no separate `history.db`), eliminating BoltDB lock conflicts on restart.

## Differences from original SpotiFLAC

| Feature | Original | Web |
|---------|----------|-----|
| Interface | Desktop (Wails) | Web browser |
| Auth | None | Jellyfin login |
| Multi-user | ❌ | ✅ |
| Watchlists + auto-sync | ❌ | ✅ |
| M3U8 Jellyfin | ❌ | ✅ |
| LAN bypass | ❌ | ✅ |
| Docker | ❌ | ✅ |
| Self-hosted | ❌ | ✅ |

## Changelog

### v1.2.14 — 2026-03-10
- **feat:** `DISABLE_AUTH_ON_LAN` — auto-login on direct LAN/localhost access
- **fix:** FFmpeg install dialog no longer appears in web mode (check deferred until authenticated)
- **fix:** Spotify URLs with `intl-fr/` prefix and `?si=` parameter now work for albums and artists

### v1.2.13
- **fix:** Playlist names now correctly resolved from Spotify metadata (`Owner.Name` field)

### v1.2.12
- **fix:** Watchlist stats — `missing = total - downloaded` (was incorrectly showing 100% failed)

### v1.2.11
- **fix:** Playlist name refresh triggered immediately after adding a watchlist

### v1.2.10
- **fix:** Build error from v1.2.9 (history handlers missing userID)

### v1.2.8
- **feat:** `SyncWatchlist` — syncs new tracks AND retries failed ones in one operation
- **feat:** Watchlist stats redesign (total / downloaded / missing)
- **fix:** Playlist name loading on WatchlistPage

### v1.2.7
- **fix:** `main.go` graceful shutdown — `os.Exit` replaced by `SIGTERM`, proper `app.shutdown(ctx)` with timeout

### v1.2.6
- **fix:** `CloseJobManager` idempotent (sync.Once)
- **fix:** `songLinkSem` context-aware (no goroutine leak on shutdown)
- **fix:** `EnqueueBatch` deduplication check
- **fix:** `ClearAllJobs` key-by-key deletion (no bucket drop)
- **fix:** `recoverPendingJobs` resets progress to 0

### v1.2.5
- **fix:** `watcher.go` race condition on `TrackIDs` + `saveWatchlist`
- **fix:** `cleanupTicker` properly consumed in select loop
- **fix:** M3U8 track ordering uses `sort.Slice`
- **fix:** `EnqueueBatch` called before `generateM3U8`

### v1.2.0 → v1.2.4
- **fix:** CORS middleware ordering
- **fix:** All 8 history handlers pass `userID` from JWT (not from request body)
- **fix:** `handleMe` uses `GetUserFromContext`
- **fix:** `CleanupOldJobs` admin-only
- **fix:** Path traversal protection on file upload
- **fix:** `UserID` isolation in `HistoryItem` and `FetchHistoryItem`

### v1.1.7 → v1.1.9
- **fix:** History DB merged into `jobs.db` — eliminates BoltDB lock conflicts on Docker restart
- **fix:** `generateM3U8` reads per-user settings from BoltDB (not global `config.json`)

### v1.1.2 → v1.1.6
- **fix:** Auth guard on all RPC pollers — eliminates 401 flood on page load (fail2ban safe)
- **fix:** `SettingsPage` syncs from backend BoltDB on mount
- **fix:** Refresh button triggers `ForceSyncWatchlist` on all playlists
- **fix:** `started_at` negative timestamp on old jobs (Go zero-time → 0)

## Disclaimer

This project is for **educational and private use only**.

**SpotiFLAC Web** is not affiliated with Spotify, Tidal, Qobuz, Amazon Music, Deezer, Jellyfin or any other service. You are solely responsible for ensuring your use complies with your local laws and the Terms of Service of the respective platforms.

## Credits

- [afkarxyz/SpotiFLAC](https://github.com/afkarxyz/SpotiFLAC) — original project
- [MusicBrainz](https://musicbrainz.org) · [LRCLIB](https://lrclib.net) · [Song.link](https://song.link) · [hifi-api](https://github.com/binimum/hifi-api)