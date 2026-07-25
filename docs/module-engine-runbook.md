# Module Engine — Runbook (next steps)

The fork-as-engine path, sequenced so your hands-on push is minimal. The fork sits
**under** the Go app: our app sends "URL → get FLAC"; the fork resolves + downloads.
Full rationale in [module-version-integration.md](module-version-integration.md);
what dies from dev2 in [module-engine-migration.md](module-engine-migration.md).

Legend: 🧑 **you** (needs your GitHub / server / Docker) · ✅ **prepared** (ready on this branch) · 🤖 **me** (next, on your word)

---

## Phase 0 — Fork + prove the loop  🧑  *(your whole push is here; ~30 min)*

This is the **go / no-go gate**. If one track downloads through the shim, the entire
foundation works. If not, the failure is cheap and nothing else has been built.

1. **Fork** `BartolomeoRusso9/SpotiFLAC-Module-Version` → your GitHub. Clone it.
2. **Add** the 4 prepared files into the fork under `engine/` (copy from this branch's `engine/`):
   `shim.py` · `Dockerfile` · `requirements.txt` · `FORK-MAINTENANCE.md`.
   ✅ No edits needed unless a `VERIFY` note fires.
3. **Add the upstream remote** now (for cheap future syncs — see FORK-MAINTENANCE.md):
   ```bash
   git remote add upstream https://github.com/BartolomeoRusso9/SpotiFLAC-Module-Version
   ```
4. **Build + run the engine alone:**
   ```bash
   docker build -f engine/Dockerfile -t spotiflac-engine .
   docker run --rm -p 8080:8080 -v "$PWD/out:/staging" spotiflac-engine
   ```
5. **Prove one track** (Deezer — no DRM, cleanest test), in another shell:
   ```bash
   curl -s localhost:8080/health
   curl -s -X POST localhost:8080/download \
     -H 'content-type: application/json' \
     -d '{"spotify_url":"https://open.spotify.com/track/2Fxmhks0bxGSBdJ92vM42m","services":["deezer"],"quality":"LOSSLESS","out_dir":"/staging"}'
   ```
   → expect `{"status":"ok","file":"/staging/<hash>/....flac", ...}` and a real FLAC in `./out/`.

   ✅ **Signature pre-verified (2026-07-23)** against the real `AsyncSpotiFLAC.__init__`: `output_dir`,
   `services`, `quality`, `enrich_metadata`, `embed_lyrics` all exist, it's an async context manager,
   and `pip install .` works (`pyproject.toml` = setuptools + `[project]`, package `SpotiFLAC` **1.5.4**).
   So the shim should run as written.

   ⚠️ **The real risk to watch instead — a GUI dep in a headless image.** The package's runtime deps
   include **`pywebview`** (desktop GUI). If `from SpotiFLAC import AsyncSpotiFLAC` pulls the GUI path
   at import time, the container will fail to start or the first POST will throw. Two fixes, in order:
   1. import the submodule directly instead of the package root (e.g. `from SpotiFLAC.client import AsyncSpotiFLAC`);
   2. if it's a missing system lib, add it to the `apt-get` line in `engine/Dockerfile`.
   Both are one-line changes, isolated to the shim/Dockerfile — their core is untouched.

**Tell me the result.** Green → I start Phase 1. Red → we read the log together, no other work wasted.

---

## Phase 1 — Wire into the Go app  🤖  *(I do this on your green)*

- ✅ `backend/engine/client.go` — Go HTTP client to the shim. **Done.**
- 🤖 Wire it into the worker: route the target provider → `engine.Client.Download`, then
  **ingest** the returned file (move from `/staging` → library, our tags + `SPOTIFY_ID`,
  catalog, SSE, clean the job dir).
- 🤖 Add the `spotiflac-engine` service to your real dev2 compose (+ `/staging` volume,
  `ENGINE_URL`), **keeping** the `turnstile-solver` service until everything is validated.
- 🤖 Add engine `/health` to `api_status.go`; set the anonymous auto-order to
  `qobuz,deezer,amazon`.

---

## Phase 2 — Cut over per provider, then remove  *(staged; nothing deleted early)*

Order from [module-engine-migration.md](module-engine-migration.md):
1. Deezer via engine in **prod** → validate → delete `backend/deezer/`.
2. Qobuz via engine → validate → delete `backend/qobuz/`.
3. Amazon via engine → validate → delete `backend/amazon/`.
4. All delegated + proven → remove Selenium/`community` + the `turnstile-solver` service.
5. Trim `songlink` (ISRC now via `spotify.GetTrackISRC` for BYOT-Tidal only).

Rollback anytime before a deletion = route the provider back to the Go `switch`.

---

## Ongoing — the whole point: low maintenance

- A route dies → upstream usually fixes it → `git fetch upstream && git merge upstream/main`
  → rebuild → re-run Phase 0 step 5. Clean, because our diff is just the 4 added files.
- You touch engine code only when a flag can't get you there (see FORK-MAINTENANCE.md).
- You keep owning the library layer (queue, catalog, tags, watcher, Jellyfin, M3U8) — small and stable.

---

## What's prepared vs. what's yours

| Piece | State |
|---|---|
| `engine/shim.py` (adapter, tagging-off, anon order) | ✅ ready |
| `engine/Dockerfile` (build from fork source, non-root) | ✅ ready |
| `engine/requirements.txt` · `engine/FORK-MAINTENANCE.md` | ✅ ready |
| `backend/engine/client.go` (Go → shim) | ✅ ready |
| Fork on GitHub · build · run · prove one track | 🧑 Phase 0 |
| Worker wiring · ingestion · compose · health · auto-order | 🤖 Phase 1, on your green |

Nothing is committed — everything is working-tree on `feat/module-engine`.
