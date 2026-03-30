# Deployment

## Requirements

- Docker + Docker Compose
- A running [Jellyfin](https://jellyfin.org) instance (used for authentication)
- FFmpeg — **bundled in the Docker image**, no separate installation needed

---

## Quick Start

```bash
git clone https://github.com/methammer/SpotiFLAC
cd SpotiFLAC
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
    image: ghcr.io/methammer/spotiflac:latest
    container_name: spotiflac
    restart: unless-stopped
    stop_grace_period: 30s
    ports:
      - "6890:6890"
    environment:
      - JELLYFIN_URL=http://your-jellyfin-host:8096
      - JWT_SECRET=change-me-to-a-random-32-char-string
      # - DISABLE_AUTH_ON_LAN=true   # see authentication.md
    volumes:
      - /path/to/music:/home/nonroot/Music
      - /path/to/config:/home/nonroot/.SpotiFLAC
```

### Volume mapping

| Container path | Purpose |
|----------------|---------|
| `/home/nonroot/Music` | Where downloaded files are stored |
| `/home/nonroot/.SpotiFLAC` | Config, database, token cache |

> Both volumes must be writable by the container user (`uid 65532 nonroot`). On Linux: `chown -R 65532:65532 /path/to/config /path/to/music`

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `JELLYFIN_URL` | `http://localhost:8096` | URL of your Jellyfin server, reachable from inside the container |
| `JWT_SECRET` | *(insecure built-in)* | Secret for JWT signing — **always change in production** |
| `DISABLE_AUTH_ON_LAN` | `false` | Auto-login on direct LAN access — see [authentication.md](authentication.md) |

---

## Data Storage

All persistent state lives in the config volume (`/home/nonroot/.SpotiFLAC`):

| File | Description |
|------|-------------|
| `jobs.db` | BoltDB — download jobs, watchlists, users, settings, history |
| `jwt_secret` | Auto-generated JWT signing key (created on first run) |
| `tidal_token.json` | Cached Tidal PKCE token, if authenticated |

> **Backup:** a single `cp jobs.db jobs.db.bak` is sufficient to snapshot all state.

---

## Reverse Proxy

### Nginx / SWAG

```nginx
location / {
    proxy_pass http://localhost:6890;
    proxy_http_version 1.1;

    # Required for SSE (download progress stream)
    proxy_set_header Connection '';
    proxy_buffering off;
    proxy_cache off;

    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 0;
}
```

> The `X-Forwarded-For` header set by the proxy is what prevents `DISABLE_AUTH_ON_LAN` from triggering on internet requests — never remove it.

### Caddy

```caddyfile
spotiflac.example.com {
    reverse_proxy localhost:6890 {
        header_up X-Forwarded-For {remote_host}
        flush_interval -1
    }
}
```

### Important: SSE timeouts

The download queue uses **Server-Sent Events** (`/api/v1/jobs/stream`). Ensure your proxy does not buffer or time out long-lived connections:
- Nginx: `proxy_read_timeout 0;` + `proxy_buffering off;`
- Caddy: `flush_interval -1`
- AWS ALB / CloudFront: set idle timeout ≥ 300s

---

## Updating

```bash
docker compose pull
docker compose up -d
```

SpotiFLAC uses rolling Docker tags (`latest` + per-version `vX.Y.Z`). BoltDB migrations are automatic.

---

## Building from Source

```bash
# Requirements: Go 1.22+, Bun

# 1. Build the frontend
cd frontend
bun install
bun run build
cd ..

# 2. Build the Go binary (frontend is embedded via go:embed)
go build -o spotiflac .

# Run
./spotiflac
```

```bash
# Or with Docker
docker build -t spotiflac:local .
docker run -p 6890:6890 \
  -e JELLYFIN_URL=http://your-jellyfin:8096 \
  -e JWT_SECRET=mysecret \
  -v /path/to/music:/home/nonroot/Music \
  -v /path/to/config:/home/nonroot/.SpotiFLAC \
  spotiflac:local
```

---

## Jellyfin co-location (same host)

If Jellyfin runs on the same Docker host, use the Docker host IP or a shared network instead of `localhost`:

```yaml
services:
  spotiflac:
    # ...
    environment:
      - JELLYFIN_URL=http://jellyfin:8096   # if on same Docker network
      # or
      - JELLYFIN_URL=http://172.17.0.1:8096 # Docker bridge host IP
    networks:
      - jellyfin_network

networks:
  jellyfin_network:
    external: true
```

---

## Troubleshooting

See [troubleshooting.md](troubleshooting.md).
