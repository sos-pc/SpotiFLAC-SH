# Engine image = THIS fork (the download engine) + our HTTP shim.
# Built FROM the fork's source, not a pinned pip blob — so you run exactly the code
# you can read and mod. Heavy deps (python/node/ffmpeg) live here; the Go app stays
# a lean static binary. Never published on the host — internal compose network only.
#
# Build context = the fork root. This file lives at engine/Dockerfile in the fork.
FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates ffmpeg flac nodejs \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Install the fork's own package from source, then its root requirements.txt,
# then ours.
#
# All three are needed: `pip install .` only pulls pyproject's [project]
# dependencies, which are NOT the engine's full runtime set — the root
# requirements.txt additionally lists `nodriver`, and `requests` is missing from
# both (our engine/requirements.txt fills that gap; see the note there).
# Installing only the package fails at import time.
COPY . .
RUN pip install --no-cache-dir . \
    && pip install --no-cache-dir -r requirements.txt \
    && pip install --no-cache-dir -r engine/requirements.txt

# Non-root, same uid as the Go service so FLACs written to the shared /staging
# volume are readable/movable by it. HOME set explicitly (numeric uid, no passwd).
#
# /staging is created and chowned here on purpose, even though it is always a
# mount point: when Docker first populates an EMPTY named volume it copies the
# image directory's ownership at that path. Without this the volume is born
# root:root and this container (uid 1000) cannot write the downloaded file into
# it — and neither can the Go app, which has to move the file out and delete the
# job dir. A bind mount hides the problem only because you chown it by hand.
RUN mkdir -p /home/nonroot/.cache /staging && chown -R 1000:1000 /home/nonroot /app /staging
USER 1000:1000
ENV HOME=/home/nonroot
ENV ENGINE_PORT=8080

EXPOSE 8080
# shim.py lives at engine/shim.py; --app-dir puts it on sys.path. The engine package
# itself was pip-installed above, so `from SpotiFLAC import ...` resolves globally.
CMD ["sh", "-c", "uvicorn shim:app --app-dir /app/engine --host 0.0.0.0 --port ${ENGINE_PORT}"]
