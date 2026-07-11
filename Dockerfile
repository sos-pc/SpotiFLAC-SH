# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 — Build frontend
# ─────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1@sha256:e10577f0db68676a7024391c6e5cb4b879ebd17188ab750cf10024a6d700e5c4 AS frontend-builder

WORKDIR /app/frontend

COPY frontend/ .
RUN bun install --frozen-lockfile
RUN bun run build

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 — Build backend Go
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm@sha256:18aedc16aa19b3fd7ded7245fc14b109e054d65d22ed53c355c899582bbb2113 AS backend-builder

WORKDIR /app

# Dépendances Go (cachées si go.mod/go.sum inchangés)
COPY go.mod go.sum ./
RUN go mod download

# Sources Go
COPY *.go ./
COPY backend/ ./backend/

# Frontend buildé (nécessaire pour l'embed)
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# Retirer les dépendances wails orphelines + compiler
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o spotiflac .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3 — Runtime
#
# trixie-slim, not bookworm-slim (Debian 12, now oldstable): a Trivy scan of
# the bookworm-based image found 6 CRITICAL/6 HIGH CVEs, almost all in the
# old ffmpeg 5.1.9 build and its codec libraries (libaom, libavcodec, ...) —
# several already marked will_not_fix by Debian on bookworm specifically.
# trixie ships a current ffmpeg build instead of relying on backports that
# aren't coming. CGO_ENABLED=0 in the builder stage means the Go binary
# itself is statically linked and unaffected by the base image's glibc.
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
        tzdata && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -u 1000 -m -s /bin/bash nonroot
USER nonroot
WORKDIR /home/nonroot

COPY --from=backend-builder /app/spotiflac /usr/local/bin/spotiflac

EXPOSE 6890

CMD ["/usr/local/bin/spotiflac"]
