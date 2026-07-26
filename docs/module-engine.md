# Download engine — reference

> **📘 Reference — verified in production 2026-07-25/26.** Describes what the code
> does today, not what was planned. The planning documents that led here are in
> [archive/](archive/); several of their predictions turned out wrong and are
> corrected below (see [What we got wrong](#what-we-got-wrong)).

The engine is a fork of a third-party Python downloader, run as a sidecar, that
resolves a Spotify track and fetches its audio. Our Go service keeps everything
that gives a track its identity: naming, tags, catalog, queue, watcher, auth, SSE.

Related: [FORK-MAINTENANCE.md](../engine/FORK-MAINTENANCE.md) (keeping the fork cheap) ·
[docker-compose.engine.example.yaml](../docker-compose.engine.example.yaml)

---

## 1. Why

Our provider layer relied on **one community proxy per provider**, each a single
point of failure needing hand-maintenance. Qobuz failed ~80% of the time
(ISRC-first resolution, which Qobuz barely indexes), Deezer's only proxy was
dead, Amazon was blocked.

The engine turns each provider into a **self-healing multi-route path** — it
carries several stream hosts per provider and falls through them — and resolves
tracks by text search + scoring + duration validation instead of ISRC lookup.
That resilience, maintained upstream, is the point; "more providers" is not.

Forking (rather than vendoring a black box or reimplementing in Go) keeps the
code readable and moddable while offloading most maintenance.

---

## 2. Architecture

```
┌── spotiflac (Go) ──────────────┐        ┌── spotiflac-engine ─────────────┐
│ queue · watcher · catalog      │  HTTP  │ forked Python module            │
│ auth · SSE · M3U8 · tagging    │───────▶│ + our FastAPI shim (engine/)    │
│                                │◀───────│                                 │
└────────────┬───────────────────┘        └───────────────┬─────────────────┘
             └──────── shared volume /staging ────────────┘
                       (same path in both, uid 1000)

┌── turnstile-solver ────────────┐
│ supplies the community session │  ← used by the Go native paths
└────────────────────────────────┘
```

- The engine is **never published on the host** — compose network only. It runs
  third-party code, so it gets no inbound exposure.
- **No TTY** (`stdin_open: false`, `tty: false`): one engine route falls back to
  prompting for a manual grant on stdin. Without a TTY it fails fast on EOF; with
  one it would hang a download waiting for a human.
- Both containers run as **uid 1000** so files in `/staging` are readable and
  movable by the app. `/staging` is created and chowned in the engine image, or
  Docker would create the named volume as root.

### The contract

Deliberately **engine-agnostic** — nothing in it names the upstream project, so
swapping the engine is a change to `engine/shim.py` alone, with no Go change.

```
POST /download  {spotify_url, services[], quality, out_dir, allow_fallback}
             →  {status: "ok"|"error", file, error, log}
GET|HEAD /health → {status: "ok"}
```

---

## 3. Turning it on

Two variables, **both required**:

| Variable | Meaning |
|---|---|
| `ENGINE_URL` | where the shim lives, e.g. `http://spotiflac-engine:8080` |
| `ENGINE_SERVICES` | comma-separated providers delegated to the engine, e.g. `qobuz,deezer` |

If either is missing, **every provider keeps its native Go path, byte for byte**.
Removing a name from `ENGINE_SERVICES` is the full rollback — no rebuild, no code
change. Only `tidal`/`qobuz`/`amazon`/`deezer` have any effect; other names are inert.

**`ENGINE_SERVICES` is not a chain.** The user's `autoOrder` chain still belongs to
our Go: `ExecuteDownload` walks it and asks the engine for **one provider at a
time**. Handing the whole chain to the engine would cost us the per-provider
native fallback and the per-provider log attribution.

---

## 4. How a download flows

```
ExecuteDownload walks the user's autoOrder chain
  └─ runService(svc)
       ├─ engineHandles(svc)?  → POST /download  services=[svc]
       │      ├─ hi-res failed + AllowFallback + provider has tiers
       │      │        → retry once at LOSSLESS
       │      └─ still failed → fall through to the NATIVE path for the same provider
       └─ otherwise            → native Go downloader
  └─ ingestion (ours): /staging → library, our tags, catalog, SSE
```

**Why the native fallback exists — and why it has never paid off.** The intent was
complementarity: the engine matches far better but could not answer the community
challenge headlessly, while our native path matches poorly yet rides a real
session obtained by the solver.

⚠️ **Measured, 2026-07-26: it has never produced a file.** Across every download
since the engine went live — ~10 Qobuz successes through the engine — the native
path was reached 3 times and failed all 3, always at the same place:

```
[Engine] Failed, retrying on the native path service=qobuz
[Auto] Service failed, trying next service=qobuz err=track not found for ISRC: …
```

It dies at *resolution*, in `searchByISRC` — the very ~80%-broken mechanism the
engine was adopted to escape. It never reaches the point where its session would
matter, so it currently buys latency and nothing else.

Two things must be true for it to earn its place: its resolution has to work
(text search + scoring, or the archived MusicBrainz pipeline), **and** the engine
has to still be grant-blocked. The second is going away as the engine gains its
own grant path. Decide its fate on evidence, not on the original intent.

**Ingestion is ours** (`backend/engine_ingest.go`):
- filename and folders come from the same helpers the native downloaders use
- the file is streamed with `providerutil.DownloadToFileAtomic` — a plain rename
  would fail across the two Docker volumes (`EXDEV`)
- we write our cover, genre and tags, including **`SPOTIFY_ID`**, which
  `meta.BuildSpotifyIDIndex` needs to rebuild M3U8s from disk
- **if tagging fails the file is deleted**: tagging parses the container, so a
  failure means the payload is not the audio it claims to be, and `ExecuteDownload`'s
  own cleanup cannot catch it (it keys off a filename we return empty on error)
- the job directory is removed afterwards; the shim removes it on its own failures

The engine is told **not to tag** (`enrich_metadata=False`, `embed_lyrics=False`).
An engine-sourced file is indistinguishable from a natively downloaded one.

---

## 5. Verified in production

| | Result |
|---|---|
| **Qobuz** | works — the provider that failed ~80% on our ISRC path. Full-length FLAC, correct tags, ~6–30 s |
| **Deezer** | works — 4/4 on a Charlie Parker album (46–61 MB each). One route 502s, others take over |
| **Ingestion** | verified on disk: our folder tree, our tags, `SPOTIFY_ID`, genre, cover, staging cleaned |
| **Tracks absent from the catalogue** | still fail (`TRACK_NOT_FOUND`) — no setting changes that |

**Hi-res is opportunistic.** Much of the catalogue exists only in 16/44.1, and a
strict 24-bit request on such a track fails outright (observed: a bare HTTP 500
retried six times on a track whose `track/get` says `hires=false`,
`maximum_bit_depth=16`, `streamable=true` — it was there all along). Keep
`HI_RES_LOSSLESS` selected: the retry takes CD quality when hi-res does not exist.
`[Engine] hi-res failed, retrying at CD quality` in the logs tells you which
tracks those are.

---

## 6. Known limits

- **Some engine routes need an interactive grant** and fail on EOF in a
  container. Others succeed, so most downloads go through. The solver supplies
  the community session for the **native** paths — but since native Qobuz never
  resolves (§4), that value currently reduces to **Amazon**. An earlier revision
  claimed the solver was load-bearing for Qobuz; it is not.
- The engine and our Go path use **different community endpoints**, so a session
  obtained by one does not serve the other.
- **Do not delegate Tidal.** The engine's Tidal is anonymous — previews only —
  while ours uses a personal Premium token.
- **Amazon** through the engine means its DRM code runs. That is an operator
  decision, taken by adding `amazon` to `ENGINE_SERVICES`.
- The Go engine client times out at **10 minutes** per download. Community hosts
  have been observed dropping to ~78 kB/s, so a very large file on a bad day can
  hit that ceiling; the engine keeps downloading and leaves an unclaimed job dir.

---

## 7. Operating it

**Deploy** (both containers when the shim changed):
```bash
cd <compose-dir>/engine-src && git pull
docker compose -f <compose-file> pull spotiflac
docker compose -f <compose-file> up -d --build spotiflac-engine spotiflac
```

**Health** — the engine appears as an `Engine` row in Settings → APIs, probing
`/health`. It is listed only when `ENGINE_URL` is set, so an install without the
engine shows no permanently-down phantom service.

**Logs** — the engine's own errors are collapsed to one line per distinct problem
(tracebacks stripped, consecutive duplicates suppressed). Set
`ENGINE_LOG_TRACEBACKS=1` on the engine service to get the full output back.
Note these lines go to `docker logs`, not to our Debug Logs page: Python's
logging handlers capture `sys.stderr` before our redirect can wrap it.

**Rollback** — remove the provider from `ENGINE_SERVICES` and `up -d`. The native
Go path takes over immediately.

---

## 8. What we got wrong

Recorded because the planning docs still assert these, and because each was
falsified by production rather than by argument:

| Claim | Reality |
|---|---|
| "Delegating retires the Selenium/Turnstile solver" | **False.** Some routes still require a manual grant; the solver became load-bearing for the native fallback. |
| "Deezer is dead on both sides, drop it" | **False.** Deezer works through the engine — one route is down, the rest carry it. Judged on a single track and generalised. |
| "Qobuz native becomes dead code" | **Half-wrong twice.** It is wired as the fallback — but measurement shows it has never succeeded (3 invocations, 3 failures at ISRC resolution), so calling it "load-bearing" was itself an unmeasured claim. See §4. |
| "The anonymous chain must be reordered away from tidal-first" | Was called moot at one point; it is not — `defaultAutoOrder` is still tidal-first. Unresolved, low impact while a token is present. |
| Spotify carries no ISRC | **False.** `spotify.GetTrackISRC` fetches it from Spotify's own metadata service. |

The pattern: every wrong call came from generalising a single observation.
Measure the specific track before concluding about the provider.
