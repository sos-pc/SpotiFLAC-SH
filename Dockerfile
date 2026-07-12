# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 — Build frontend
# ─────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1@sha256:e10577f0db68676a7024391c6e5cb4b879ebd17188ab750cf10024a6d700e5c4 AS frontend-builder

WORKDIR /app/frontend

# Baked into the UI's displayed version (see frontend/vite.config.ts) — CI
# passes the real semver/branch tag here; a plain `docker build` with no
# --build-arg falls back to vite.config.ts's own "dev" default.
ARG APP_VERSION
ENV APP_VERSION=${APP_VERSION}

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
# Pinned to a specific dated release tag AND a specific versioned asset
# filename (BtbN/FFmpeg-Builds cuts a dated release most days, each carrying
# assets like ffmpeg-N-<rev>-g<hash>-linux64-lgpl.tar.xz). Deliberately not
# the generic "ffmpeg-master-latest-linux64-lgpl.tar.xz" name: that filename
# only exists under BtbN's separate "latest" release, which is a single tag
# force-updated in place on every build — exactly the mutable-reference
# pattern this repo avoids elsewhere (see the trivy-action commit-SHA pin
# below). The dated tag + exact asset name can't be silently repointed.
# Verified against the checksums.sha256 published alongside that same tag
# rather than a hash transcribed into this file, since the build environment
# fetching it has real internet access and can verify
# tarball-against-its-own-published-checksum at build time.
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
ARG FFMPEG_ASSET=ffmpeg-N-125519-g300cac3078-linux64-lgpl.tar.xz
WORKDIR /tmp/ffmpeg
RUN curl -fLO "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_BUILD_TAG}/${FFMPEG_ASSET}" && \
    curl -fLO "https://github.com/BtbN/FFmpeg-Builds/releases/download/${FFMPEG_BUILD_TAG}/checksums.sha256" && \
    grep "${FFMPEG_ASSET}\$" checksums.sha256 | sha256sum -c - && \
    tar xf "${FFMPEG_ASSET}" --strip-components=1 && \
    chmod +x bin/ffmpeg bin/ffprobe

# Minimal root skeleton for the scratch runtime below (which has no shell to
# mkdir/chown itself): the app's hardcoded fallback paths (/home/nonroot/Music,
# /home/nonroot/.SpotiFLAC — see api_admin.go, watcher.go, main.go) plus /tmp
# (server.go uses os.TempDir() for upload staging), all owned by uid/gid 1000
# to match the non-root USER the runtime image runs as.
RUN mkdir -p /rootfs/home/nonroot/Music /rootfs/home/nonroot/.SpotiFLAC /rootfs/tmp && \
    chown -R 1000:1000 /rootfs

# ─────────────────────────────────────────────────────────────────────────────
# Stage 4 — Runtime
#
# FROM scratch, not a Debian base: the Go binary is CGO_ENABLED=0 (statically
# linked, no glibc dependency) and ffmpeg/ffprobe above are static builds —
# nothing in this image actually needs a Linux distro, a package manager, or
# a shell. The only non-negotiable OS-level artifact a Go program still needs
# for outbound TLS is a CA certificate bundle, copied in as a plain data file
# from the ffmpeg-static stage (which already installed ca-certificates to
# fetch ffmpeg over HTTPS) — everything else that a Debian base would have
# provided (bash, coreutils, apt, tzdata) is unused: no code shells out
# anymore (see backend/util/system.go), and grep across the codebase found
# no timezone-database lookups (time.LoadLocation). Result: zero OS packages
# in the final image, so a vulnerability scanner has zero OS-level CVE
# surface left to report — only our own Go binary and the ffmpeg binary,
# both already scanned clean.
#
# Docker/Kubernetes always inject /etc/resolv.conf, /etc/hosts and
# /etc/hostname into a running container regardless of what the image itself
# contains, so DNS resolution works here even though the image ships none of
# those files.
# ─────────────────────────────────────────────────────────────────────────────
FROM scratch

COPY --from=ffmpeg-static /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=ffmpeg-static --chown=1000:1000 /rootfs/home /home
COPY --from=ffmpeg-static --chown=1000:1000 /rootfs/tmp /tmp

COPY --from=backend-builder /app/spotiflac /usr/local/bin/spotiflac
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffprobe /usr/local/bin/ffprobe

USER 1000:1000
WORKDIR /home/nonroot

# scratch has no /etc/passwd, so the container runtime can't look up a home
# directory for numeric UID 1000 the way it could when the previous
# debian-based image's `useradd -m` gave USER an /etc/passwd entry to
# resolve — it falls back to HOME=/, which broke every os.UserHomeDir()
# caller (config dir, default music path, ffmpeg path, Tidal auth token
# storage) into resolving paths under / instead of /home/nonroot, where
# these mounted volumes and the skeleton dirs above actually live. Setting
# HOME explicitly restores the same resolution the debian image gave for
# free.
ENV HOME=/home/nonroot

EXPOSE 6890

CMD ["/usr/local/bin/spotiflac"]
