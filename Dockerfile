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
# Stage 3 — Static ffmpeg
#
# Not `apt-get install ffmpeg`: on both bookworm and trixie that pulls ~30
# transitive shared-library dependencies (libavcodec/libavformat's own codec
# libs, glib, Mesa/libgbm/libglx for GPU hwaccel, mbedtls, libssh, perl for
# packaging scripts) — a Trivy scan found 60 CRITICAL/HIGH CVEs across those
# packages on trixie (12 on bookworm), almost none of them in code this app,
# a headless audio-only service, ever executes (no GPU accel, no SSH, no XML).
# A statically-linked ffmpeg build has no runtime library dependencies of its
# own to carry those CVEs.
#
# Pinned to a specific dated release tag (BtbN/FFmpeg-Builds cuts one most
# days), not a floating "latest" pointer — same reasoning as the trivy-action
# commit-SHA pin elsewhere in this repo: an immutable reference can't be
# silently repointed. Verified against the checksums.sha256 published
# alongside that same tag rather than a hash transcribed into this file,
# since the build environment fetching it has real internet access and can
# verify tarball-against-its-own-published-checksum at build time.
#
# LGPL build, not GPL: this app only needs libmp3lame (LGPL-licensed LAME)
# plus FFmpeg's own native aac/alac encoders (see backend/audio/ffmpeg.go) —
# no GPL-only codec (x264/x265/...) is used anywhere, so the GPL variant's
# larger footprint buys nothing here.
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df AS ffmpeg-static

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        xz-utils && \
    rm -rf /var/lib/apt/lists/*

ARG FFMPEG_BUILD_TAG=autobuild-2026-07-11-13-13
WORKDIR /tmp/ffmpeg
RUN curl -fLO "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_BUILD_TAG}/ffmpeg-master-latest-linux64-lgpl.tar.xz" && \
    curl -fLO "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_BUILD_TAG}/checksums.sha256" && \
    grep 'linux64-lgpl\.tar\.xz$' checksums.sha256 | sha256sum -c - && \
    tar xf ffmpeg-master-latest-linux64-lgpl.tar.xz --strip-components=1 && \
    chmod +x bin/ffmpeg bin/ffprobe

# ─────────────────────────────────────────────────────────────────────────────
# Stage 4 — Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -u 1000 -m -s /bin/bash nonroot
USER nonroot
WORKDIR /home/nonroot

COPY --from=backend-builder /app/spotiflac /usr/local/bin/spotiflac
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffprobe /usr/local/bin/ffprobe

EXPOSE 6890

CMD ["/usr/local/bin/spotiflac"]
