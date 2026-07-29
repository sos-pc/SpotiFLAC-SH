# Deployment

> **⚠️ Ce document décrit le déploiement *minimal viable*, pas un déploiement durci.**
> Le compose d'exemple ci-dessous publie un port et ne définit ni `TRUST_PROXY_HEADERS`, ni
> `cap_drop`, ni `read_only`. Derrière un reverse proxy, ces choix ont des **conséquences réelles et
> mesurées** — dont un déni de service trivial sur le login, et un `/api/upload` qui ne peut pas
> fonctionner. Lire **[deployment-hardening.md](deployment-hardening.md)** avant de mettre en ligne :
> il porte la configuration vérifiée en production, l'ordre dans lequel appliquer les changements
> (il compte), et ce qui reste ouvert.

## Requirements

- Docker + Docker Compose
- A running [Jellyfin](https://jellyfin.org) instance (used for authentication)
- FFmpeg + FFprobe — **bundled in the Docker image**, no separate installation needed

---

## Quick Start

```bash
git clone https://github.com/sos-pc/SpotiFLAC-SH
cd SpotiFLAC-SH
cp docker-compose.example.yaml docker-compose.yaml
# Edit docker-compose.yaml (see below)
docker compose up -d
```

Open `http://your-server:6890` and log in with your Jellyfin credentials.

---

## docker-compose.yaml

```yaml
services:
  spotiflac:
    image: ghcr.io/sos-pc/spotiflac-sh:latest
    container_name: spotiflac
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "6890:6890"
    environment:
      - JELLYFIN_URL=http://your-jellyfin-host:8096
      - JWT_SECRET=change-me-to-a-random-32-char-string
      # - DISABLE_AUTH_ON_LAN=true   # see authentication.md
    user: "1000:1000"
    volumes:
      - /path/to/music:/home/nonroot/Music
      - spotiflac_config:/home/nonroot/.SpotiFLAC

volumes:
  spotiflac_config:
    name: spotiflac_config
```

### Volume mapping

| Container path | Type | Purpose |
|----------------|------|---------|
| `/home/nonroot/Music` | Bind mount (host path you choose) | Where downloaded files are stored (default `downloadPath`) — a real host path, so Jellyfin (or anything else) can read the files directly |
| `/home/nonroot/.SpotiFLAC` | Docker-managed named volume | Config, BoltDB, JWT secret, Tidal token cache |

Both need to be writable by **uid 1000**, the non-root user the image runs as (`USER 1000:1000` in `Dockerfile`) — but each gets there differently:

- **`/path/to/music` (bind mount):** on **first run**, if this host directory doesn't exist yet, Docker creates it as `root` before starting the container — and the app, running as non-root with no shell to fix that itself, fails with `permission denied`. Before the first `docker compose up`, create it and set ownership yourself:
  ```bash
  mkdir -p /path/to/music
  sudo chown -R 1000:1000 /path/to/music
  ```
- **`spotiflac_config` (named volume):** nothing to do. Docker only auto-populates a brand new named volume's contents (and ownership) from whatever the image already has at that path — and the image ships that directory pre-created and owned by uid 1000 — so the volume comes up with the right permissions automatically. This is exactly why the config directory uses a named volume instead of a second bind mount: it's the one persistent path with no reason to be a specific host-visible folder, so it can dodge this whole class of first-run permission errors for free.

> If you're upgrading an older deployment that bind-mounts `/home/nonroot/.SpotiFLAC` to a host folder, you don't have to switch — the bind mount still works, it just needs the same manual `chown` as the music folder on a fresh setup. Migrating to the named volume is optional; if you do, copy your existing config folder's contents into the new volume first (see the backup command below, run in reverse) so you don't lose `jobs.db`.

#### Choosing where `spotiflac_config` actually lives on disk

By default (above), Docker picks the storage location itself, under its
own managed area (typically `/var/lib/docker/volumes/...`) — that's what
gets the automatic-permissions benefit. Find it with:
```bash
docker volume inspect spotiflac_config --format '{{ .Mountpoint }}'
```

If you'd rather pick the exact folder yourself — e.g. to have it covered
by a backup job that already targets a specific directory — point the
volume at a host path via `driver_opts` instead:
```yaml
volumes:
  spotiflac_config:
    name: spotiflac_config
    driver: local
    driver_opts:
      type: none
      o: bind
      device: /path/to/your/backed-up/folder/spotiflac-config
```
This trades away the automatic permissions: Docker now treats the volume
as a plain pointer to that folder, the same as a bind mount, so it needs
the same one-time setup as the music folder before the first
`docker compose up`:
```bash
mkdir -p /path/to/your/backed-up/folder/spotiflac-config
sudo chown -R 1000:1000 /path/to/your/backed-up/folder/spotiflac-config
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `JELLYFIN_URL` | `http://localhost:8096` | URL of your Jellyfin server, **reachable from inside the container** (so not `localhost` if Jellyfin runs on the host). |
| `JWT_SECRET` | *(auto-generated)* | Secret for JWT signing. If unset, SpotiFLAC generates 32 random bytes on first start and writes them to `<config>/jwt_secret` (mode `0600`). Set this env var to share a secret across replicas, or to inject one from a secret manager. |
| `DISABLE_AUTH_ON_LAN` | `false` | Auto-login on direct LAN access — see [authentication.md](authentication.md). |

---

## Data Storage

All persistent state lives in the config volume (`/home/nonroot/.SpotiFLAC`):

| File | Purpose |
|------|---------|
| `jobs.db` | BoltDB single-file database. Buckets: `jobs`, `watchlist`, `users`, `apikeys`, `history`, `fetch_history`. Two orphans survive in databases created before 2026-07-28 — `proxy_discovery` and `api_proxies`, left from the removed proxy-discovery and proxy-configuration features. Nothing reads either; together they are a few hundred bytes, so they are left in place rather than migrated away. |
| `jwt_secret` | Auto-generated JWT signing key (mode `0600`). Skipped when `JWT_SECRET` env var is set. |
| `tidal_token.json` | Cached Tidal Device Code token (mode `0644`). Created on successful auth, deleted on disconnect or refresh failure. Auto-refreshed before expiry. |
| `config.json` | Legacy global settings (read-only fallback for users with no per-user settings yet). New deployments should not need this — settings are stored per-user inside `jobs.db`. |

> **Backup:** with the example compose file's named volume (`spotiflac_config`), there's no host folder to `cp` directly — go through a throwaway container instead:
> ```bash
> docker run --rm -v spotiflac_config:/data -v "$(pwd)":/backup busybox \
>   tar czf /backup/spotiflac-config-backup.tar.gz -C /data .
> ```
> Restore the same way, with `tar xzf` instead of `czf`. If you're still bind-mounting `/home/nonroot/.SpotiFLAC` to a host folder (see the note above), a plain `cp jobs.db jobs.db.bak` on that host folder still works.

---

## Reverse Proxy

The download queue uses **Server-Sent Events** (`GET /api/v1/jobs/stream`) and the artist-discography search uses another SSE endpoint (`GET /api/v1/search/stream`). Both are long-lived. Your reverse proxy must:

1. Disable response buffering for these endpoints. The server sets `X-Accel-Buffering: no`, which nginx honours on its own — verified against a real SWAG deployment whose `proxy.conf` leaves `proxy_buffers` at its default.
2. Read timeout: `/api/v1/jobs/stream` emits a `: keepalive` SSE comment every 30s while idle (see `sseHeartbeatInterval` in `sse.go`), so any `proxy_read_timeout` comfortably above 30s is fine — SWAG's default of 240s works untouched. **Before 2026-07-16 there was no heartbeat**, and an idle stream was cut every 240s, silently reconnecting and re-sending the whole 48h job snapshot each time; see [deployment-hardening.md §5.2](deployment-hardening.md). `/api/v1/search/stream` has no heartbeat by design — it streams its results and ends.

The Go server itself is configured for indefinite long requests:

- `ReadTimeout: 0` (downloads can be hours)
- `WriteTimeout: 0`
- `IdleTimeout: 120s`

### Nginx / SWAG

```nginx
location / {
    proxy_pass http://localhost:6890;
    proxy_http_version 1.1;

    # Required for SSE
    proxy_set_header Connection '';
    proxy_buffering off;
    proxy_cache off;

    # Standard headers
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 0;
}
```

> The `X-Forwarded-For` header set by the proxy is what prevents `DISABLE_AUTH_ON_LAN` from triggering on internet requests — never strip it.

> **Login rate limiting behind a reverse proxy:** set `TRUST_PROXY_HEADERS=true` so the login rate limiter (`POST /api/v1/auth/login`) keys off the real client IP from `X-Forwarded-For`/`X-Real-IP` instead of the proxy's own IP. This is **off by default** — trusting those headers unconditionally would let any client on the LAN (or anything sharing a Docker network with the container) forge a fresh IP on every request and bypass the lockout entirely. Only set it when every request genuinely passes through a proxy you control that overwrites these headers (as in the examples on this page); never set it if the app is reachable directly.

### Caddy

```caddyfile
spotiflac.example.com {
    reverse_proxy localhost:6890 {
        header_up X-Forwarded-For {remote_host}
        flush_interval -1
    }
}
```

### AWS ALB / CloudFront

Set the **idle timeout to at least 300 s** (CloudFront origin timeout: max 60 s without an L7 plan — consider an ALB instead). Disable response buffering / compression for the `/api/v1/*` paths.

---

## Updating

```bash
docker compose pull
docker compose up -d
```

SpotiFLAC uses the rolling `latest` Docker tag plus per-version `vX.Y.Z` tags. BoltDB schema migrations are applied automatically on first run after upgrade (`InitHistoryDBShared`, bucket creation in `NewJobManager` and `NewAuthManager`).

### Post-upgrade: back-fill `SPOTIFY_ID` tags on legacy files

After upgrading from a build that did not embed `SPOTIFY_ID` automatically, run **once** as an admin:

```bash
curl -s -X POST http://your-server:6890/api/v1/admin/retag-legacy \
  -H "Authorization: Bearer <admin-jwt-or-api-key>" | jq
```

This walks every Done/Skipped job in BoltDB whose file still exists on disk and writes the `SPOTIFY_ID` tag in place. Idempotent — safe to re-run.

---

## Building from Source

The Go binary embeds the built frontend via `go:embed all:frontend/dist`. The frontend **must** be built before `go build`, otherwise the embed directive will fail or embed an empty tree.

```bash
# Requirements: Go 1.26+, Bun (frontend bundler — uses Vite under the hood)

# 1. Build the frontend
cd frontend
bun install --frozen-lockfile
bun run build      # -> writes to frontend/dist
cd ..

# 2. Build the Go binary
go mod tidy
CGO_ENABLED=0 go build -ldflags="-s -w" -o spotiflac .

./spotiflac
```

```bash
# Or with Docker (multi-stage build: bun → go → ffmpeg fetch → distroless/cc)
docker build -t spotiflac:local .
docker run -p 6890:6890 \
  -e JELLYFIN_URL=http://your-jellyfin:8096 \
  -e JWT_SECRET=mysecret \
  -v /path/to/music:/home/nonroot/Music \
  -v /path/to/config:/home/nonroot/.SpotiFLAC \
  spotiflac:local
```

> **Note (2026-07-15).** Stage 4 was `FROM scratch` until a regression showed the FFmpeg build is
> **not** fully static: its codec libraries are bundled, but the executables still link glibc and
> libgcc dynamically (verified on the pinned asset), and `scratch` ships no ELF loader — so every
> `exec` of ffmpeg failed for three days while the image built green. The runtime is now
> `distroless/cc`, which supplies exactly those libraries and nothing else, and CI now runs the
> binaries instead of only copying them. See
> [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md).

The Dockerfile pipeline (4 stages):

1. **Stage 1 (`oven/bun:1`)** — install frontend dependencies and run `bun run build`.
2. **Stage 2 (`golang:1.26-bookworm`)** — copy frontend `dist`, run `go mod tidy`, build a static binary (`CGO_ENABLED=0`, `-s -w`).
3. **Stage 3 (`debian:bookworm-slim`, build-only)** — downloads an FFmpeg/FFprobe build (BtbN/FFmpeg-Builds, pinned by a dated release tag + checksum verification) instead of `apt install ffmpeg` — deliberately avoids pulling in ~30 transitive shared-library dependencies that a Trivy scan found carried dozens of CVEs this headless audio-only service never actually exercises (GPU hwaccel, SSH, XML parsing, etc.). This stage's own Debian packages (`curl`, `ca-certificates`, `xz-utils`) never reach the runtime image. **The CVE argument here assumes that build is self-contained — it is not; see the warning above.**
4. **Stage 4 (`gcr.io/distroless/cc`, pinned by digest)** — the actual runtime: the Go binary, the two FFmpeg binaries, a CA bundle, and glibc + libgcc — **no shell, no package manager, no apt database**. `distroless/base` would not do: it lacks `libgcc_s`, which FFmpeg needs. Runs as numeric `USER 1000:1000`; distroless ships an `/etc/passwd` but only for root and its own `nonroot` (65532), so uid 1000 still has no entry to resolve a home from and `HOME=/home/nonroot` is set explicitly via `ENV`. Because there is no shell, `docker exec spotiflac sh -c '...'` **does not work** — see [troubleshooting.md](troubleshooting.md).

Because stage 4 has no shell, `docker exec spotiflac sh -c '...'` and similar debugging commands **do not work** on this image — see [troubleshooting.md](troubleshooting.md) for the alternative.

---

## Jellyfin co-location (same host)

If Jellyfin runs on the same Docker host, use the Docker host IP or a shared network instead of `localhost` (which inside the container points to the container itself, not the host):

```yaml
services:
  spotiflac:
    # ...
    environment:
      - JELLYFIN_URL=http://jellyfin:8096           # if on same Docker network
      # or
      - JELLYFIN_URL=http://172.17.0.1:8096          # Docker bridge gateway
      # or
      - JELLYFIN_URL=http://host.docker.internal:8096 # Docker Desktop
    networks:
      - jellyfin_network

networks:
  jellyfin_network:
    external: true
```

---

## CI/CD

The repository ships with two GitHub Actions workflows:

- **`.github/workflows/docker.yml`** — runs on every `v*.*.*` tag. Builds the frontend, runs `go test ./... -v`, builds and pushes a multi-arch Docker image to `ghcr.io/<owner>/spotiflac-sh` with tags `{version}`, `{major}.{minor}` and `latest`. Creates a GitHub Release with auto-generated notes.
- **`.github/workflows/upstream-check.yml`** — periodic check against `spotbye/SpotiFLAC` upstream. Surfaces drift in tracked source files. See `check-upstream.sh` at the repo root for the local equivalent.

---

## Troubleshooting

See [troubleshooting.md](troubleshooting.md).
