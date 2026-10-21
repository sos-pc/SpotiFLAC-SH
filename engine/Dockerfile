# Engine image = upstream's published package + our HTTP shim.
#
# Upstream is `mornye` (PyPI, github.com/mornyeapp/Mornye-Python). Until
# 2026-10-20 the same project shipped as `SpotiFLAC` from
# BartolomeoRusso9/SpotiFLAC-Module-Version; after Spotify's legal notice to the
# SpotiFLAC author it was renamed, its history and tags were reset, and the
# `SpotiFLAC` package stopped at 5.0.6. The API this image calls carried over
# unchanged apart from the class name (AsyncSpotiFLAC -> AsyncMornye).
#
# Build context is THIS directory (engine/), not a checkout of the engine's
# source: the core comes from PyPI. Heavy deps (python/node/ffmpeg) live here;
# the Go app stays a lean static binary. Never published on the host — internal
# compose network only.
FROM python:3.12-slim

# Runtime roles:
#   ffmpeg/flac   audio processing
#   nodejs        runs the provider extensions, which are JavaScript bundles
#   patch         applies engine/patches/ below (absent from python:3.12-slim)
#
# What is deliberately NOT here any more: xvfb, chromium and its fonts, and the
# --no-sandbox wrapper the SpotiFLAC image needed around Chromium. They existed
# for one thing — the browser the engine drives through Cloudflare Turnstile when
# a provider gateway challenges a new session. `mornye` takes an external solver
# natively (TURNSTILE_SOLVER_URL, "used instead of the built-in browser"), which
# is the service this deployment already runs; the SpotiFLAC image only reached
# it through hooks/solver_fallback.py, after its own browser had failed.
#
# So this image does not carry a browser at all. Chromium was most of its size,
# and it ran without its sandbox in a container that executes third-party
# JavaScript. The trade-off, stated plainly: with no TURNSTILE_SOLVER_URL set, or
# with that service down, a session that needs a challenge cannot be obtained, and
# downloads through the providers that need one fail until it is back.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg flac nodejs patch \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Which version to install. CI resolves it from PyPI — the same call its
# staleness check already makes — and passes it here, so the image tag, the OCI
# label and the installed package cannot disagree. Left empty by default so a
# manual `docker build` still works, taking whatever is current.
ARG MORNYE_VERSION=

# One install, where SpotiFLAC needed two.
#
# The SpotiFLAC wheel declared fewer dependencies than the project imported, so
# this step used to fetch the matching tag's requirements.txt from GitHub and
# install that as well. `mornye` declares them all: its 1.0.3 wheel's
# Requires-Dist is the same set as that tag's requirements.txt (compared
# 2026-10-21). Dropping the second fetch also drops the build's dependency on
# upstream's git tags — which is what stopped this image on 2026-10-20, when the
# repository was renamed and every old tag disappeared at once.
#
# If a later release under-declares again, the import check and `pip check`
# further down fail the build; nothing ships half-installed.
RUN if [ -n "$MORNYE_VERSION" ]; then SPEC="mornye==$MORNYE_VERSION"; else SPEC="mornye<2"; fi \
    && pip install --no-cache-dir "$SPEC" \
    && echo "installed mornye $(python -c 'import importlib.metadata as m; print(m.version("mornye"))')"

# Our own deps (FastAPI + uvicorn for the shim).
COPY requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt

# Build-time patches applied to the installed package, in site-packages.
#
# Patches are the right shape for bug fixes the upstream source needs corrected,
# and the wrong one for behaviour we merely want to add: a patch that stops
# applying blocks the entire update chain. There are none at the moment.
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
RUN SITE="$(python -c 'import mornye, pathlib; print(pathlib.Path(mornye.__file__).parent.parent)')" \
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

# No runtime hooks. The only one, hooks/solver_fallback.py, wrapped SpotiFLAC's
# community verification to fall back to TURNSTILE_SOLVER_URL; `mornye` reads
# that variable itself. See the package list at the top.

# Fail the build rather than ship an image that cannot import its own core.
# Cheap, and it catches the exact class of breakage that reached production
# before: a dependency the wheel does not declare.
#
# It used to also `import nodriver, pydoll` by name. That hardcoded UPSTREAM's
# dependency list into ours, and upstream changed it: v3.6.0 dropped nodriver
# from requirements.txt. The build then failed 52 times over nine days, on a
# module the package no longer uses. The traceback said so itself — the error
# named only nodriver, which means `from SpotiFLAC import AsyncSpotiFLAC` had
# already succeeded on the line before.
#
# So the core import stays and the names go. What replaces them is a claim that
# does not encode anyone else's choices: `pip check` asserts the installed set
# is internally consistent, which is the real question behind "did the install
# work". Verified against a real image built by this file: "No broken
# requirements found."
#
# This cannot see a module imported lazily inside a function. Nothing here
# could. contract-check.py imports everything shim.py imports, starts every
# JS bundle and checks every host call those bundles make; a real download is
# what proves the rest.
RUN python -c "from mornye import AsyncMornye; print('import check OK')" \
    && python -m pip check

# The import check above proves the package loads. It does not prove upstream
# still takes the arguments shim.py passes it — and that is the failure this
# image is most exposed to, because a scheduled build publishes `:latest`
# without a human reading the changelog. A renamed keyword would build green and
# fail on the first download. See contract-check.py for the full reasoning.
# ── The download providers ────────────────────────────────────────────────
#
# They are not in the wheel. SpotiFLAC 3.0.0 replaced its Python providers with
# JavaScript extensions that an extension manager downloads from a registry, and
# `mornye` bundles no registry at all — by design, it says: the operator names
# one in `MORNYE_REGISTRIES` (formerly SPOTIFLAC_REGISTRIES). A headless image
# that sets none installs nothing, resolves no provider, and fails EVERY download
# with `ValueError: No valid providers found in: [...]` — which names the service
# list, the one thing that is not wrong. See docs/engine-3.0-impact-plan.md.
#
# Installing them HERE rather than letting them arrive on first use, because
# runtime install gives up three things this image is built to have:
#
#   - reproducibility. MORNYE_VERSION pins the wheel; it does not pin
#     qobuz-web@1.2.23. Two builds of this file a week apart would ship
#     different download code under the same tag.
#   - a startup that does not depend on GitHub. The extensions land in
#     ~/.mornye/extensions, which no volume persists, so every container
#     recreation would re-fetch all of them.
#   - failing at build instead of on the first download.
#
# ARG, deliberately not ENV: the variable must NOT survive into the running
# image. With it unset at runtime the manager skips its registry check entirely
# and uses what is installed here — measured, and the point of doing this at
# build time. An operator who wants runtime updates can set it in compose,
# which is then a choice rather than a default.
#
# The registry is the Mornye one (formerly zarzet/SpotiFLAC-Extension; the old
# URL still redirects, this is its canonical name). It serves executable
# JavaScript from a third-party repository, and its sha256 comes from the same
# registry that serves the file — good against corruption, not against a
# compromised registry. That is a real exposure and it is the reason this is
# pinned into an image someone can inspect rather than fetched fresh on every
# start.
ARG MORNYE_REGISTRIES=https://raw.githubusercontent.com/mornyeapp/Mornye-Extension/main/registry.json
RUN HOME=/home/nonroot MORNYE_REGISTRIES="$MORNYE_REGISTRIES" python -c "\
from mornye.extensions.manager import ExtensionManager; \
m = ExtensionManager(auto_install_downloads=True); \
import os; \
names = sorted(os.listdir(m.ext_dir)); \
print('extensions installed:', ', '.join(names) or '(none)'); \
raise SystemExit(0 if names else 'no extension installed - the registry served nothing usable')"

# Hold a bundle the host cannot run at its last runnable version, when needed.
#
# The registry serves extensions written for the mobile app, whose host runtime
# can move ahead of this module's. When a bundle calls a host method the bridge
# does not define, contract-check.py below fails the build and names the call;
# pinning that extension here is one of the two ways out. No pin is needed on
# `mornye`: its bridge implements everything the current bundles call. See
# pin-extensions.py.
COPY pin-extensions.py /tmp/pin-extensions.py
RUN HOME=/home/nonroot python /tmp/pin-extensions.py && rm /tmp/pin-extensions.py

# What the build just installed, written into the image beside shim.py.
#
# Same ARG, so the provenance recorded is the registry actually used. It runs
# here rather than later because this is the only point where both halves are
# available: the registry document, and the freshly unpacked extensions.
COPY record-extensions.py /tmp/record-extensions.py
RUN HOME=/home/nonroot MORNYE_REGISTRIES="$MORNYE_REGISTRIES" \
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
RUN mkdir -p /home/nonroot/.cache /staging \
    && chown -R 1000:1000 /home/nonroot /app /staging
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

# No DISPLAY and no entrypoint: both only served the Xvfb virtual display the
# browser needed (see the package list at the top). Upstream's own --web mode
# runs without them as well.
EXPOSE 8080
# shim.py sits at /app/shim.py; --app-dir puts it on sys.path. The engine package
# was pip-installed above, so `from mornye import ...` resolves globally.
CMD ["sh", "-c", "uvicorn shim:app --app-dir /app --host 0.0.0.0 --port ${ENGINE_PORT}"]
