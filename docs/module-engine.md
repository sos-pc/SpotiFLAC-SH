# Download engine — reference

> **📘 Reference — last verified against the code and production on 2026-08-04**,
> engine 1.6.0. Describes what the code does today, not what was planned. The
> planning documents that led here are in [archive/](archive/); several of their
> predictions turned out wrong, as did several claims this document itself made —
> all corrected in [What we got wrong](#8-what-we-got-wrong).

The engine is a third-party Python downloader, run as a sidecar, that resolves a
Spotify track and fetches its audio. Our Go service keeps everything that gives a
track its identity: naming, tags, catalog, queue, watcher, auth, SSE.

It used to be a fork we merged from; it is now upstream's published package plus
our shim and patches, built into an image by CI. See
[engine/README.md](../engine/README.md) for the build and
[upstream-tracking-plan.md](upstream-tracking-plan.md) for why the fork went.

Related: [engine/README.md](../engine/README.md) ·
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

Installing their published package (rather than vendoring a black box or
reimplementing in Go) keeps the code readable and patchable while offloading most
maintenance. It began as a fork; see [engine/README.md](../engine/README.md) for
why that was retired without losing either property.

---

## 2. Architecture

```
┌── spotiflac (Go) ──────────────┐        ┌── spotiflac-engine ─────────────┐
│ queue · watcher · catalog      │  HTTP  │ upstream Python package         │
│ auth · SSE · M3U8 · tagging    │───────▶│ + our FastAPI shim (engine/)    │
│                                │◀───────│                                 │
└────────────┬───────────────────┘        └───────────────┬─────────────────┘
             └──────── shared volume /staging ────────────┘
                       (same path in both, uid 1000)

                                          ┌── turnstile-solver ─────────────┐
                                          │ supplies the community session  │
                                          └───────────────△─────────────────┘
                                    TURNSTILE_SOLVER_URL  │
                                          set on the ENGINE, not the app
```

- The engine is **never published on the host** — compose network only. It runs
  third-party code, so it gets no inbound exposure.
- **No TTY** (`stdin_open: false`, `tty: false`): one engine route falls back to
  prompting for a manual grant on stdin. Without a TTY it fails fast on EOF; with
  one it would hang a download waiting for a human.
- Both containers run as **uid 1000** so files in `/staging` are readable and
  movable by the app. `/staging` is created and chowned in the engine image, or
  Docker would create the named volume as root.
- The engine image **carries a browser** — `xvfb`, `chromium`, `fonts-liberation`,
  `DISPLAY=:99`, Xvfb started by its entrypoint — because several provider routes
  drive Chromium through pydoll. This mirrors upstream's own Dockerfile. It makes
  the image ~480 MB against the app's ~114 MB, and it requires **`shm_size: 1g`**
  in compose plus no `read_only` on that service. See
  [engine/README.md](../engine/README.md) for why `/usr/bin/chromium` is a
  wrapper script.

### The contract

Deliberately **engine-agnostic** — nothing in it names the upstream project, so
swapping the engine is a change to `engine/shim.py` alone, with no Go change.

```
POST /download  {spotify_url, services[], quality, out_dir, allow_fallback}
             →  {status: "ok"|"error", file, error, log}
GET|HEAD /health → {status: "ok", engine_version, revision}
GET /providers/health?services=a,b
             →  {pending, checked_at, providers{name:{ok, reachable, total,
                 latency_ms, detail}}, extensions{ok, detail},
                 skipped_malformed}
```

**`status: "ok"` means the file was parsed as audio, not merely produced.** The
shim reads it with mutagen before answering — the same library the engine's own
tagger uses — because the engine will happily report success on a payload it
just failed to parse (§5). Anything an engine puts behind this contract has to
clear that bar.

The extra `/health` fields are additive; a consumer decoding only `{status}` is
unaffected.

**`/providers/health` answers a different question from `/health`.** `/health` is
liveness — the sidecar replies. It says nothing about whether the providers
behind it can deliver, and on 2026-08-07 it read `ok` for hours while Qobuz had
3 reachable mirrors out of 48, Deezer's only resolver answered `403` and Amazon's
only host refused connections. `/apis/status` now carries one row per provider
from this endpoint, so the status board distinguishes "our deployment is broken"
from "upstream's fleet is down" without anyone running a command.

Three properties are deliberate:

- **A warm call never blocks; a cold one waits about two seconds.**
  Stale-while-revalidate on a 5-minute cache, so a stale answer is served
  immediately and refreshed behind the request. But a *cold* call waits up to 6 s
  for the first sample, because the first version did not and the result was a
  status board that showed no provider rows on the first load after a deploy and
  cached that emptiness for 30 s — indistinguishable from a broken feature, and
  reported as one. The wait is affordable: measured 2.2 s for 13 real probes on
  2026-08-08. The "ten seconds" this was first built around was the cost of
  importing SpotiFLAC in a throwaway `docker exec`, not the probes.
- **It asks the engine what *it* would try.** We do not keep a list of provider
  hosts; upstream resolves them from a registry fetched at runtime, so any list
  of ours would be wrong within days.
- **`skipped_malformed` is not noise, it is a count of an upstream bug.**
  `get_qobuz_endpoints` returns the raw registry value, which is a *string* for
  some categories, and `health_check.py` iterates it directly — so every
  character becomes an "endpoint" (`a/prepare`, `b/prepare`, …). 35 of them on
  2026-08-07. `providers/qobuz.py` wraps the same value in a list before use,
  which is the handling `health_check.py` forgot; the shim filters non-`http`
  entries rather than patching their file, because a patch is a liability at
  every upstream release and this costs nothing.

**`quality` is canonical, not per-provider.** One of:

```
HI_RES_LOSSLESS · HI_RES · LOSSLESS · HIGH · LOW · DOLBY_ATMOS
```

`backend.engineQualityFor` translates our per-provider dialects into that set —
Tidal's own names, Qobuz's numeric format IDs, and the `"flac"` literal
`resolveAudioFormat` still returns for Deezer. Sending canonical names is what
keeps this contract independent of any one engine's alias table: an engine is
free to accept `27` or `flac`, but it must understand the six names above.

That mapping is the *only* place a provider's quality vocabulary is allowed to
appear on the delegated path, the same way `TidalQualityFor` owns it for the
native one.

---

## 3. Turning it on

| Variable | Required | Meaning |
|---|---|---|
| `ENGINE_URL` | yes | where the shim lives, e.g. `http://spotiflac-engine:8080` |
| `ENGINE_SERVICES` | yes | comma-separated providers delegated to the engine, e.g. `qobuz,deezer,amazon,tidal` |
| `ENGINE_STAGING_DIR` | no | the shared path, **default `/staging`**. Must name the same directory in both containers |

Only `tidal`/`qobuz`/`amazon`/`deezer` have any effect in `ENGINE_SERVICES`;
other names are inert.

⚠️ **Since v4.0.0 these are not optional in practice.** Qobuz, Amazon and Deezer
have no native Go path left, so without `ENGINE_URL` and their names in
`ENGINE_SERVICES` they return *"only available through the download engine"*.
Only Tidal still works engine-less, and only with a token. Earlier revisions of
this section said omitting the variables left "every provider on its native Go
path, byte for byte" — that was true when the wrappers existed, and removing a
name is no longer a rollback but a way to disable a provider.

**`ENGINE_SERVICES` is not a chain.** The user's `autoOrder` chain still belongs
to our Go: `ExecuteDownload` walks it and asks the engine for **one provider at a
time**. Handing the whole chain over would cost the per-provider log attribution
and the BYOT ordering in §4.

---

## 4. How a download flows

```
ExecuteDownload walks the user's autoOrder chain
  └─ runService(svc)          backend/downloader.go
       ├─ byotConfigured(svc)?   (Tidal with a personal token)
       │      → NATIVE first, engine as the backup if it fails
       ├─ EngineHandles(svc)?
       │      → POST /download services=[svc]   — the engine owns it,
       │        failure included; no native fallback
       │        └─ hi-res failed + AllowFallback + provider has tiers
       │                 → retry once at LOSSLESS
       └─ otherwise            → native Go downloader
  └─ ingestion (ours): /staging → library, our tags, catalog, SSE
```

**There is only one native downloader left: Tidal.** Qobuz, Amazon and Deezer
were anonymous community-proxy wrappers that the engine replaced, and their Go
code is gone. Asking for one of them without it in `ENGINE_SERVICES` returns

```
qobuz is only available through the download engine — add it to ENGINE_SERVICES
```

which names the variable rather than reading as a bug.

**Credentials beat anonymity, so BYOT inverts the order.** For Tidal with a
token, native runs *first* and the engine backs it up: the engine's Tidal is
tokenless, so asking it first only spends time on a request known to be weaker.
For everything else the engine goes first and there is nothing behind it — the
chain simply moves to the next provider.

> An earlier revision of this document showed the opposite flow, with every
> engine failure falling through to a native path for the same provider. That
> was true when the wrappers still existed. It is worth knowing what the fallback
> was measured to do before it went: reached 3 times, failed 3 times, always in
> `searchByISRC` — the ~80%-broken mechanism the engine was adopted to escape. It
> never once produced a file.

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
| **Tidal (tokenless)** | works, but only via the `ext:tidal-web` fallback — see below |
| **Ingestion** | verified on disk: our folder tree, our tags, `SPOTIFY_ID`, genre, cover, staging cleaned |
| **Tracks absent from the catalogue** | still fail (`TRACK_NOT_FOUND`) — no setting changes that |

**The engine reports success on files it cannot parse.** Measured 2026-08-04, on
five consecutive tracks: Deezer's primary resolver died mid-stream, the partial
response was left at a `.flac` path, the engine logged
`is not a valid FLAC file`, downgraded it to `Tagging failed (non-fatal)`, and
returned `Successful: 1`.

Two independent checks now stand between that and the library. The shim parses
the payload with mutagen before returning `ok` (§2), and our Go ingestion deletes
anything that fails tagging (§4). Before the first of those existed, the Go check
was the only thing catching it — and it caught it four times in five tracks. Do
not treat `status: "ok"` from any engine as evidence that a file is audio.

**Dead upstream endpoints are the normal failure mode, not an incident.**
Resolved from their runtime registry on 2026-08-04: three of nine Qobuz hosts no
longer resolve at all (`qbz.afkarxyz.qzz.io`, `qobuz.spotbye.qzz.io`,
`qobuz.squid.wtf`), and Deezer's primary route `deezer.anandserver.cfd` fails on
every attempt, leaving `ext:deezer` to carry the provider. That table is fetched
at runtime from an encrypted gist, so upstream can replace a dead host **without
publishing a release** and a container restart picks it up — which is also why
the same provider fails differently from one day to the next. Nothing to fix on
our side; check the hosts before assuming a regression.

**Tidal's direct API route is dead upstream, and the fallback carries it.**
Measured 2026-07-31, three tracks, all successful:

```
✗ tidal · proxy · HTTP 404 Not Found
✗ tidal ·       · HTTP 410 - The v1 download API has been retired.
  [tidal] UNAVAILABLE: All Tidal APIs failed (of 2 total, 0 in cooldown).
⚠️  Fallback: switching to backup extension (ext:tidal-web)...
```

Both endpoints in upstream's registry are gone — one 404s, the other answers
**410 Gone**: its v1 download API has been retired. Nothing on our side fixes
that; it is their endpoint table, and they are active enough that it will
probably move on its own.

Two things this is worth knowing for. First, `ext:tidal-web` is not a nicety —
it is the only working tokenless Tidal path, so the JS extension route and the
`nodejs` package the image installs for it are load-bearing. Second, the error
finally *says* something: it used to read `no Tidal APIs configured`, which was
our own omission (the API list was never primed — fixed in `shim.py`'s
`_prime_tidal_apis`). Reaching a real endpoint and being told it is retired is a
different, more useful failure — and it got faster, 58 s and 28 s against 68 s
before.

**Hi-res is opportunistic.** Much of the catalogue exists only in 16/44.1, and a
strict 24-bit request on such a track fails outright (observed: a bare HTTP 500
retried six times on a track whose `track/get` says `hires=false`,
`maximum_bit_depth=16`, `streamable=true` — it was there all along). Keep
`HI_RES_LOSSLESS` selected: the retry takes CD quality when hi-res does not exist.
`[Engine] hi-res failed, retrying at CD quality` in the logs tells you which
tracks those are.

---

## 6. Known limits

- **No Go code reads `TURNSTILE_SOLVER_URL`.** It went with `backend/community`.
  The solver still has exactly one consumer — `signed_session_*.py` inside the
  engine, reached through our patch and through code upstream adopted itself —
  so it belongs on the **engine** service and nowhere else. Setting it on the app
  container does nothing.
- **Delegating Tidal is safe when a token is configured.** BYOT runs the native
  path first and only falls back to the engine (§4), so the engine's tokenless
  Tidal cannot displace a Premium session. Without a token, the engine's Tidal is
  previews-only via `ext:tidal-web`. An earlier revision said "do not delegate
  Tidal" without that qualification.
- **Amazon** through the engine means its DRM code runs. That is an operator
  decision, taken by adding `amazon` to `ENGINE_SERVICES`.
- The Go engine client times out at **10 minutes** per download
  (`backend/engine/client.go`). Community hosts have been observed dropping to
  ~78 kB/s, so a very large file on a bad day can hit that ceiling; the engine
  keeps downloading and leaves an unclaimed job dir.
- **`defaultAutoOrder` is `tidal-qobuz-amazon-deezer`** — still tidal-first. With
  a token that is correct. Without one it spends the first attempt on a
  previews-only provider.

---

## 7. Operating it

**Deploy** — both images are pulled, nothing is built on the host:
```bash
docker compose -f <compose-file> pull
docker compose -f <compose-file> up -d
```

The engine image is rebuilt and published by CI whenever upstream releases, so a
`pull` is all that is needed to pick up an engine update. To pin or roll back,
replace `latest` with a version tag in the compose file — every image carries the
SpotiFLAC version it contains in its `org.opencontainers.image.version` label.

**Health** — the engine appears as an `Engine` row in Settings → APIs, probing
`/health`. It is listed only when `ENGINE_URL` is set, so an install without the
engine shows no permanently-down phantom service.

**Which build is running** — the shim says so at startup and on `/health`:

```
INFO:     engine shim: SpotiFLAC 1.6.0, image d0b573a
```

The revision is the commit that built the image, baked in as `ENGINE_REVISION`
because OCI labels are image metadata and cannot be read from inside the
container. This exists because `docker compose pull` **without** `up -d` leaves
the previous container running and nothing used to say so.

**Logs** — the engine's own errors are collapsed to one line per distinct problem
(tracebacks stripped, consecutive duplicates suppressed). Set
`ENGINE_LOG_TRACEBACKS=1` on the engine service to get the full output back.
These lines go to `docker logs`, not to our Debug Logs page: Python's logging
handlers capture `sys.stderr` before our redirect can wrap it.

⚠️ **That paragraph was false from the day it was written until 2026-08-04.** The
filter was attached to handlers, and
`SpotiFLAC/core/progress.py::install_console_interception` runs *during* the
download — after us — removing every StreamHandler from `root` and `SpotiFLAC*`,
which destroys whatever we just attached. It then adds a `TqdmLoggingHandler`,
which extends `logging.Handler` rather than `logging.StreamHandler`, so its own
cleanup skips it and `uninstall_console_interception` never removes it: one
accumulates per download and each reprints every line. Measured 1, 2, 3, 4, 5
handlers over five downloads.

The filter now attaches at **logger** level, which survives handler replacement,
and prunes the leaked progress handlers so exactly the one upstream re-adds is
left. If duplicated lines or tracebacks ever come back, that leak is the first
place to look — upstream 1.6.0 still has it.

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
| "The engine's own errors are collapsed to one line" (§7, this document) | **False for as long as it was written.** The filter was attached to handlers upstream destroys mid-download. Fixed 2026-08-04; the mechanism is in §7. |
| "The solver is load-bearing for the native fallback" | **Obsolete.** There is no native fallback for the delegated providers any more, and no Go code reads `TURNSTILE_SOLVER_URL`. It serves the engine only. |
| "Delegating retires the solver" (revisited) | **Still false, and measured.** With Chromium finally able to start, the engine's in-container path was expected to make the external solver redundant. It did not: the solver captured a grant in 25 s on a run where the engine's own browser had not started at all. |
| "`xvfb` is CAPTCHA scaffolding, not packaging" | **False.** Upstream's own Dockerfile installs `xvfb`, `chromium` and `fonts-liberation` and calls the display "MANDATORY for Chromium even without VNC". Asserted without reading their packaging. |

The pattern: every wrong call came from generalising a single observation, or
from asserting something checkable without checking it. Measure the specific
track before concluding about the provider — and read the upstream file before
concluding about upstream.
