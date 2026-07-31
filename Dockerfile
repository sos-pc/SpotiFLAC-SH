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
# BtbN's non-`-shared` build bundles those codec libraries into the executable
# instead, so none of them are separately installed packages carrying CVEs.
#
# "Static" only in that sense, and the distinction is not academic: this comment
# used to claim the build had "no runtime library dependencies of its own",
# which was never verified and turned out false — the executables still link
# glibc and libgcc dynamically. Stage 4 was then moved to `FROM scratch` on the
# strength of that claim and every ffmpeg call broke for three days. Measured
# reality (PT_INTERP + DT_NEEDED parsed from this exact asset):
#
#   /lib64/ld-linux-x86-64.so.2 + libm libdl librt libpthread libmvec libc libgcc_s
#
# See docs/ffmpeg-runtime-regression.md.
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

# The `latest` release tag, not a dated autobuild.
#
# BtbN prunes its autobuild-* releases: only ~8 days of them exist at any time,
# so pinning to one guaranteed a broken build within about a week. It duly broke
# on 2026-07-31 — `autobuild-2026-07-11-13-13` returned 404 for the release
# itself, eleven minutes after the previous build of this branch had succeeded
# against it.
#
# `latest` is never pruned and carries stable asset filenames. Taking the
# `n8.1-latest` asset rather than `master-latest` keeps us on the 8.1 release
# line — patch updates within it, no surprise major jump.
#
# We lose exact reproducibility across time, but less than it looks: the
# checksum was always fetched from the same release as the tarball, so it
# verified the download's integrity, never which build we got. That property is
# unchanged.
ARG FFMPEG_BUILD_TAG=latest
ARG FFMPEG_ASSET=ffmpeg-n8.1-latest-linux64-lgpl-8.1.tar.xz
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
# distroless/cc, not scratch — and not a full Debian base either.
#
# This image was FROM scratch until 2026-07-15, on the stated reasoning that
# "ffmpeg is a static binary, so nothing here needs a Linux distro". That was
# wrong, and it silently broke every ffmpeg/ffprobe call for three days: BtbN's
# builds are static only in the sense that they bundle their own codec
# libraries — the executables themselves are still dynamically linked. Measured
# on the exact asset pinned above (PT_INTERP + DT_NEEDED parsed, not guessed):
#
#   PT_INTERP : /lib64/ld-linux-x86-64.so.2
#   DT_NEEDED : libm.so.6, libdl.so.2, librt.so.1, libpthread.so.0,
#               libmvec.so.1, libc.so.6, ld-linux-x86-64.so.2, libgcc_s.so.1
#
# `scratch` ships no ELF loader, so exec() failed with ENOENT — naming a file
# that exists, because the missing thing is the *interpreter*, not the binary.
# See docs/ffmpeg-runtime-regression.md.
#
# distroless/cc is glibc + libgcc — exactly that list and nothing more (verified
# by listing the image's own layers; distroless/base is NOT enough, it has no
# libgcc_s). It keeps what actually mattered about scratch: no shell, no package
# manager, no apt database. What it gives up is the absolute claim "zero OS
# packages, therefore zero CVEs for a scanner to report" — which was always
# partly an artifact anyway: Trivy enumerates OS packages and language binaries,
# so the ~115MB of codec libraries baked into ffmpeg were never visible to it.
# The 51+9 -> 0 drop measured scanner blindness as much as it measured safety.
# The real attack surface (a media decoder parsing bytes chosen by third-party
# proxies) is identical either way — the base image only decides whether it runs.
#
# Docker/Kubernetes always inject /etc/resolv.conf, /etc/hosts and
# /etc/hostname into a running container regardless of what the image itself
# contains, so DNS resolution works here even though the image ships none of
# those files.
# ─────────────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/cc-debian12@sha256:7ee09f36862efbdbf70422db263e411c2618409ca46faa555bd5b636155307df

COPY --from=ffmpeg-static /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=ffmpeg-static --chown=1000:1000 /rootfs/home /home
COPY --from=ffmpeg-static --chown=1000:1000 /rootfs/tmp /tmp

COPY --from=backend-builder /app/spotiflac /usr/local/bin/spotiflac
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=ffmpeg-static /tmp/ffmpeg/bin/ffprobe /usr/local/bin/ffprobe

USER 1000:1000
WORKDIR /home/nonroot

# Still required on distroless. It does ship an /etc/passwd, but only for root
# and its own `nonroot` user (uid 65532) — numeric UID 1000 has no entry to
# resolve, exactly as on scratch. Without this, HOME falls back to /, which
# breaks every os.UserHomeDir() caller (config dir, default music path, ffmpeg
# path, Tidal token storage) into resolving paths under / instead of
# /home/nonroot, where the mounted volumes and the skeleton dirs above live.
#
# (Staying on 1000:1000 rather than switching to distroless's 65532 on purpose:
# it is the uid documented for the bind-mounted music directory, and changing it
# would silently break every existing deployment's file ownership.)
ENV HOME=/home/nonroot

EXPOSE 6890

CMD ["/usr/local/bin/spotiflac"]
