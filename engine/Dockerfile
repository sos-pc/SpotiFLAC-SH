# Engine image = upstream's published package + our HTTP shim + our patches.
#
# Build context is THIS directory (engine/), not a checkout of the engine's
# source: the core now comes from PyPI. That is the point of retiring the fork —
# see docs/upstream-tracking-plan.md §2. Heavy deps (python/node/ffmpeg) live
# here; the Go app stays a lean static binary. Never published on the host —
# internal compose network only.
FROM python:3.12-slim

# This list is upstream's own Dockerfile list, plus `patch` (not in
# python:3.12-slim, needed below). Runtime roles, in their words:
#   ffmpeg/flac        audio processing
#   nodejs             the JS extension route (ext:tidal-web carries tokenless Tidal)
#   xvfb               the virtual display — "MANDATORY for Chromium even without VNC"
#   chromium + fonts   the browser pydoll drives, and web fonts
#
# The last three were missing here for a while: we had ported their package list
# and dropped them. Nothing in the shim revealed it, because the failure only
# appears when a provider route actually reaches for a browser, and then it reads
# `[Errno 2] No such file or directory: 'Xvfb'` — which looks like an engine bug
# rather than our packaging. Chromium is most of this image's size; it is also
# what several provider routes are.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg flac nodejs patch \
        xvfb chromium fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

# Chromium cannot start in this container without --no-sandbox, and upstream
# only passes it when running as root:
#
#     # SpotiFLAC/core/solver.py
#     _docker_flags = []
#     if os.name != "nt" and hasattr(os, "geteuid") and os.geteuid() == 0:
#         _docker_flags = ["--no-sandbox", "--disable-dev-shm-usage"]
#
# Their image runs as root, so the flags apply there. Ours runs as uid 1000 —
# which is not negotiable, it is what makes /staging shareable with the Go app —
# so the list stays empty and every launch dies with "No usable sandbox!".
# `cap_drop: ALL` blocks the userns sandbox and `no-new-privileges` blocks the
# setuid helper, so there is no sandbox left to use.
#
# Wrapping the binary applies exactly the flags upstream already applies, and
# does it in our packaging rather than in their source: nothing to re-verify on
# each release, and no patch that could rot. /usr/bin/chromium still exists, so
# anything discovering the browser by path still finds it.
#
# The trade-off, stated plainly: --no-sandbox drops Chromium's internal sandbox,
# so a compromised renderer holds container privileges. This container is
# already non-root, fully capability-dropped, no-new-privileges and unpublished.
# The alternative — handing back SYS_ADMIN or dropping no-new-privileges — is
# strictly worse. --disable-dev-shm-usage additionally makes the image
# independent of whether the operator set shm_size.
RUN mv /usr/bin/chromium /usr/bin/chromium.real \
    && printf '#!/bin/sh\nexec /usr/bin/chromium.real --no-sandbox --disable-dev-shm-usage "$@"\n' \
         > /usr/bin/chromium \
    && chmod 0755 /usr/bin/chromium

WORKDIR /app

# Which version to install. CI resolves it from PyPI — the same call its
# staleness check already makes — and passes it here, so the image tag, the OCI
# label and the installed package cannot disagree. Left empty by default so a
# manual `docker build` still works, taking whatever is current.
ARG SPOTIFLAC_VERSION=

# Two installs, and the second is not optional.
#
# The wheel's metadata declares fewer dependencies than the project actually
# imports: as of 1.5.9 `nodriver`, `pydoll-python` and `aiogram` appear only in
# the repo's requirements.txt. Without them `from SpotiFLAC import AsyncSpotiFLAC`
# raises ModuleNotFoundError outright — not a lazily-hit path, the package simply
# does not import (verified in a clean venv, 2026-07-30).
#
# So: install the package, ask it which version actually landed, then fetch THAT
# tag's requirements.txt from upstream and install that too. Upstream stays the
# source of truth for its own dependency list — we never copy it here, and it is
# precisely the file they keep fixing ("Missing requirements", "Update
# requirements.txt"). urllib rather than curl: already present, so no extra
# package, and it raises on a 404 instead of silently writing an error page.
RUN if [ -n "$SPOTIFLAC_VERSION" ]; then SPEC="SpotiFLAC==$SPOTIFLAC_VERSION"; else SPEC="SpotiFLAC<2"; fi \
    && pip install --no-cache-dir "$SPEC" \
    && V="$(python -c 'import importlib.metadata as m; print(m.version("SpotiFLAC"))')" \
    && echo "installed SpotiFLAC $V" \
    && python -c "import urllib.request,sys; urllib.request.urlretrieve('https://raw.githubusercontent.com/BartolomeoRusso9/SpotiFLAC-Module-Version/v%s/requirements.txt' % sys.argv[1], '/tmp/upstream-requirements.txt')" "$V" \
    && pip install --no-cache-dir -r /tmp/upstream-requirements.txt \
    && rm -f /tmp/upstream-requirements.txt

# Our own deps (FastAPI + uvicorn for the shim).
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

# Patches applied to the installed package, in site-packages.
#
# This is what replaces carrying a fork. A patch that stops applying fails the
# build, loudly, right here — the property a scheduled drift-checker was meant to
# provide, obtained for free and sooner. --dry-run first so a bad patch cannot
# leave a half-modified tree behind. An empty patches/ is fine: the loop no-ops.
COPY patches/ /tmp/patches/
RUN SITE="$(python -c 'import SpotiFLAC, pathlib; print(pathlib.Path(SpotiFLAC.__file__).parent.parent)')" \
    && for p in /tmp/patches/*.patch; do \
         [ -e "$p" ] || continue; \
         echo "applying $(basename "$p")"; \
         patch -p1 -d "$SITE" --dry-run < "$p" && patch -p1 -d "$SITE" < "$p"; \
       done \
    && rm -rf /tmp/patches

# Fail the build rather than ship an image that cannot import its own core.
# Cheap, and it catches the exact class of breakage that reached production
# before: a dependency the wheel does not declare.
RUN python -c "from SpotiFLAC import AsyncSpotiFLAC; import nodriver, pydoll; print('import check OK')"

COPY shim.py /app/shim.py

# Non-root, same uid as the Go service so FLACs written to the shared /staging
# volume are readable/movable by it. HOME set explicitly (numeric uid, no passwd).
#
# /staging is created and chowned here on purpose, even though it is always a
# mount point: when Docker first populates an EMPTY named volume it copies the
# image directory's ownership at that path. Without this the volume is born
# root:root and this container (uid 1000) cannot write the downloaded file into
# it — and neither can the Go app, which has to move the file out and delete the
# job dir. A bind mount hides the problem only because you chown it by hand.
#
# /tmp/.X11-unix is created here for the same reason: Xvfb runs as uid 1000 and
# has to create its socket there.
RUN mkdir -p /home/nonroot/.cache /staging /tmp/.X11-unix \
    && chown -R 1000:1000 /home/nonroot /app /staging /tmp/.X11-unix
USER 1000:1000
ENV HOME=/home/nonroot
ENV ENGINE_PORT=8080

# Matches upstream's image. Set before the entrypoint starts Xvfb on the same
# display, so solver._ensure_xvfb() sees a display and never spawns its own.
ENV DISPLAY=:99

COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080
# shim.py sits at /app/shim.py; --app-dir puts it on sys.path. The engine package
# was pip-installed above, so `from SpotiFLAC import ...` resolves globally.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["sh", "-c", "uvicorn shim:app --app-dir /app --host 0.0.0.0 --port ${ENGINE_PORT}"]
