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

# Build-time patches applied to the installed package, in site-packages.
#
# Runtime behaviour that must happen inside an upstream function belongs in
# hooks/ (below), not here — a patch that stops applying blocks the entire
# update chain, while a hook that cannot wrap degrades gracefully.  Patches
# remain the right shape for bug fixes the upstream source needs corrected.
#
# Three outcomes, not two, because "the patch does not apply" hides two
# opposite events and only one of them is bad:
#
#   the bug is gone   — upstream fixed it. Our patch is obsolete. Skip it and
#                       say so; the build MUST NOT fail, or upstream fixing our
#                       bug would freeze this image on its previous version and
#                       cut us off from every other upstream change. That is not
#                       hypothetical: it is what happened on 2026-08-15 with
#                       SpotiFLAC 3.0.0, for four consecutive builds.
#   still needed, applies    — apply it.
#   still needed, will not apply — upstream moved the code around our fix. Fail
#                       hard: an image that silently drops a bug fix is worse
#                       than no image.
#
# Only a probe can tell the first case from the third, because "did upstream
# fix this?" is a question about behaviour and `patch` only reads text. So a
# patch MAY carry a sibling <name>.probe.py — see patches/README.md — that
# exits 0 when the bug is still present and 3 when it is gone. No probe means
# "assume still needed", which is the old behaviour.
#
# --dry-run before applying, so a rejected hunk cannot leave a half-modified
# tree. `|| exit 1` on both, because a for loop reports only its LAST
# iteration's status: with two patches, the first failing and the second
# succeeding, the previous version of this loop exited 0 and published an image
# missing a fix. It was correct only while exactly one patch existed.
COPY patches/ /tmp/patches/
RUN SITE="$(python -c 'import SpotiFLAC, pathlib; print(pathlib.Path(SpotiFLAC.__file__).parent.parent)')" \
    && for p in /tmp/patches/*.patch; do \
         [ -e "$p" ] || continue; \
         name="$(basename "$p" .patch)"; \
         probe="/tmp/patches/$name.probe.py"; \
         if [ -f "$probe" ]; then \
           python "$probe"; rc=$?; \
           if [ "$rc" = 3 ]; then \
             echo "OBSOLETE PATCH: $name - upstream fixed this bug themselves."; \
             echo "  Delete engine/patches/$name.patch and its probe. Not applying it."; \
             continue; \
           fi; \
           if [ "$rc" != 0 ]; then \
             echo "PROBE BROKEN: $name.probe.py exited $rc (expected 0=present, 3=gone)" >&2; \
             exit 1; \
           fi; \
           echo "probe: bug still present, $name.patch is still needed"; \
         fi; \
         echo "applying $name.patch"; \
         patch -p1 -d "$SITE" --dry-run < "$p" || exit 1; \
         patch -p1 -d "$SITE" < "$p" || exit 1; \
       done \
    && rm -rf /tmp/patches

# Runtime hooks — loaded by shim.py at startup.  Unlike patches they do not
# modify source files and survive upstream line shifts.  A hook that cannot
# wrap logs a warning and degrades; the build still succeeds.
COPY hooks/ /app/hooks/
RUN python -c "import hooks.solver_fallback; print(f'hook solver_fallback: {\"applied\" if hooks.solver_fallback._applied else \"skipped\"}')"

# Fail the build rather than ship an image that cannot import its own core.
# Cheap, and it catches the exact class of breakage that reached production
# before: a dependency the wheel does not declare.
RUN python -c "from SpotiFLAC import AsyncSpotiFLAC; import nodriver, pydoll; print('import check OK')"

# The import check above proves the package loads. It does not prove upstream
# still takes the arguments shim.py passes it — and that is the failure this
# image is most exposed to, because a scheduled build publishes `:latest`
# without a human reading the changelog. A renamed keyword would build green and
# fail on the first download. See contract-check.py for the full reasoning.
# ── The download providers ────────────────────────────────────────────────
#
# They are not in the wheel any more. SpotiFLAC 3.0.0 deleted
# `SpotiFLAC/providers/` — eight Python modules — and replaced them with
# JavaScript extensions that an extension manager downloads from a registry.
# 1.7.3 carried that registry's URL as a hardcoded constant; 3.0.0 removed it
# and expects `SPOTIFLAC_REGISTRIES` from the environment, a `.env` file, or the
# GUI settings screen. A headless image has none of those, so it installs
# nothing, resolves no provider, and fails EVERY download with
# `ValueError: No valid providers found in: [...]` — which names the service
# list, the one thing that is not wrong. See docs/engine-3.0-impact-plan.md.
#
# Installing them HERE rather than letting them arrive on first use, because
# runtime install gives up three things this image is built to have:
#
#   - reproducibility. SPOTIFLAC_VERSION pins the wheel; it does not pin
#     qobuz-web@1.1.0. Two builds of this file a week apart would ship
#     different download code under the same tag.
#   - a startup that does not depend on GitHub. The extensions land in
#     ~/.spotiflac/extensions, which no volume persists, so every container
#     recreation would re-fetch all seven.
#   - failing at build instead of on the first download.
#
# ARG, deliberately not ENV: the variable must NOT survive into the running
# image. With it unset at runtime the manager skips its registry check entirely
# and uses what is installed here — measured, and the point of doing this at
# build time. An operator who wants runtime updates can set it in compose,
# which is then a choice rather than a default.
#
# The URL is the one 1.7.3 carried internally. It serves executable JavaScript
# from a third-party repository, and its sha256 comes from the same registry
# that serves the file — good against corruption, not against a compromised
# registry. That is a real exposure and it is the reason this is pinned into an
# image someone can inspect rather than fetched fresh on every start.
ARG SPOTIFLAC_REGISTRIES=https://raw.githubusercontent.com/zarzet/SpotiFLAC-Extension/main/registry.json
RUN HOME=/home/nonroot SPOTIFLAC_REGISTRIES="$SPOTIFLAC_REGISTRIES" python -c "\
from SpotiFLAC.extensions.manager import ExtensionManager; \
m = ExtensionManager(auto_install_downloads=True); \
import os; \
names = sorted(os.listdir(m.ext_dir)); \
print('extensions installed:', ', '.join(names) or '(none)'); \
raise SystemExit(0 if names else 'no extension installed - the registry served nothing usable')"

# What the build just installed, written into the image beside shim.py.
#
# Same ARG, so the provenance recorded is the registry actually used. It runs
# here rather than later because this is the only point where both halves are
# available: the registry document, and the freshly unpacked extensions.
COPY record-extensions.py /tmp/record-extensions.py
RUN HOME=/home/nonroot SPOTIFLAC_REGISTRIES="$SPOTIFLAC_REGISTRIES" \
    python /tmp/record-extensions.py && rm /tmp/record-extensions.py

COPY contract-check.py /tmp/contract-check.py
COPY shim.py /app/shim.py
RUN HOME=/home/nonroot python /tmp/contract-check.py && rm /tmp/contract-check.py

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

# Which commit built this image, as an ENV rather than only an OCI label: labels
# are image metadata and are not readable from inside the container, so the shim
# could not otherwise say what it is. CI passes the sha; a manual build says
# "unknown", which is honest.
#
# This exists because confirming "did my fix actually deploy?" meant inspecting
# the registry from another machine. A `docker compose pull` without `up -d`
# leaves the old container running, and nothing in the logs said so.
ARG GIT_REVISION=
ENV ENGINE_REVISION=$GIT_REVISION

# Matches upstream's image. Set before the entrypoint starts Xvfb on the same
# display, so solver._ensure_xvfb() sees a display and never spawns its own.
ENV DISPLAY=:99

COPY --chmod=0755 docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080
# shim.py sits at /app/shim.py; --app-dir puts it on sys.path. The engine package
# was pip-installed above, so `from SpotiFLAC import ...` resolves globally.
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["sh", "-c", "uvicorn shim:app --app-dir /app --host 0.0.0.0 --port ${ENGINE_PORT}"]
