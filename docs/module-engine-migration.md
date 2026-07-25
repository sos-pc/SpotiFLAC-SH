# Module Engine — Keep / Drop / Delegate / Rewire

Companion to [module-version-integration.md](module-version-integration.md). That doc
decides *whether/how* to bolt on the engine (C3: two-service compose, module + FastAPI
shim). **This** doc maps *what happens to each existing subsystem* on the
`feat/module-engine` branch.

> **Status: PLANNING.** Nothing is deleted yet. See [§Sequencing](#sequencing--safety):
> nothing gets removed until the engine is proven in prod. Go-Tidal stays permanently.

---

## The one rule that decides everything

> **Our Go owns everything *about* a track — identity, queue, catalog, playlists, tags,
> UI, auth. The engine owns one thing: fetching the audio bytes.**

Anything that existed *only to fetch bytes* → drop or delegate. Anything that gives a
track its **identity or place in the library** → keep. Every answer below follows from
this line.

---

## Subsystem map

| Subsystem | Files | Verdict | Why |
|---|---|---|---|
| Provider downloaders | `backend/{tidal,qobuz,amazon,deezer}/client.go`, the `switch` in `backend/downloader.go` | **DROP → delegate** | The engine fetches bytes. Exception: **Tidal kept** as the BYOT path. |
| Provider resolution / ISRC | `backend/songlink/`, `qobuz.searchByISRC`, `tidal.SearchTidalByName` | **DROP → delegate** | Engine resolves Spotify→provider internally (better matching). Makes the "fix Qobuz match in Go" work (Piste 1) **unnecessary**. |
| Spotify metadata scrape | `backend/spotify/` | **KEEP** | Drives the **queue UI, watchlist sync, filenames, dedup** — not just download. |
| Tagging / genre / lyrics / M3U8 | `backend/meta/` | **KEEP** | Writes the **`SPOTIFY_ID` tag M3U8 regen depends on** (`meta.BuildSpotifyIDIndex`). Engine won't. We re-tag at ingestion. |
| Jobs queue + worker | `jobs*.go`, `jobs_worker.go` | **KEEP (rewire 1 step)** | Only the worker's *download call* changes: engine client instead of the `switch`. |
| Watcher / playlist sync | `watcher*.go` | **KEEP** | Pure Spotify-side logic; unaffected. |
| Catalog DB | `backend/db/` | **KEEP** | Source of truth for tracks/library/M3U8. |
| SSE | `sse.go` | **KEEP** | Job/log fan-out unchanged. |
| Logs | `logbuffer.go`, `applog.go` | **KEEP + bridge** | Add an engine-log bridge (see Q4). |
| History | `backend/history.go` | **KEEP** | Records completed downloads. |
| Audio analysis | `backend/audio/` | **KEEP** | Quality/duration detection for history + spectrum. |
| Auth (Jellyfin/JWT) | `auth.go`, `api_auth*` | **KEEP** | Untouched. |
| Proxy config | `backend/util/proxy_config.go`, `api_proxies.go`, `proxy_discovery.go` | **SHRINK** | Drop Qobuz/Amazon/Deezer lists (engine owns hosts). **Keep Tidal** list + `tidal-uptime` discovery for BYOT Go-Tidal. |
| External status probes | `api_status.go` | **REWIRE** | Drop musicdl/spotbye/deezmate probes. Probe **engine `/health` + Tidal**. |
| Selenium / Turnstile solver | `backend/community/*` (turnstile branch) | **DROP** | Engine solves its own challenges (ALTCHA PoW / token exchange, no browser). Delete after prod-proven. |
| Settings | `frontend settings.ts` + backend wiring | **REWIRE** | See Q5. |

---

## Your questions, answered

### Q1 — The downloaders: gone?
Yes, except Tidal. The `switch req.Service` in `backend/downloader.go` collapses to:

```
Tidal + valid personal token  → Go Tidal path (BYOT, proven full-FLAC)
everything else               → POST engine /download
```

The `backend/{qobuz,amazon,deezer}` packages and the Tidal *search/proxy* paths become
dead code — **deleted only after the engine is prod-proven**, not on day one.

### Q2 — Metadata: keep ours or delegate too? → **KEEP.**
Metadata is two different things; neither should move to the engine:
- **Spotify scrape** (`backend/spotify/`) drives the UI queue, watchlist expansion,
  filenames and dedup. The engine can't feed those — it's a black box that returns a file.
- **Tagging** (`backend/meta/`) writes our **`SPOTIFY_ID` tag**, which is what regenerates
  M3U8 playlists from the filesystem (`meta.BuildSpotifyIDIndex`). The engine tags with
  *its* scheme and never writes `SPOTIFY_ID` → **letting it tag would silently break
  playlists and catalog consistency.**

So: tell the engine **not to tag/enrich** (`embed_lyrics=False`, `enrich_metadata=False`),
and **re-tag at ingestion** with our pipeline. Cheap, because we ingest from a per-job
directory anyway.

### Q3 — The API: rewire needed?
The **frontend-facing API stays the same** (jobs, SSE, settings, auth, watchlists, catalog).
What changes is *internal*:
- **Worker download step** → engine client (`POST /download`) + a small ingestion step.
- **`api_proxies.go`** shrinks: Qobuz/Amazon/Deezer proxy config disappears from the UI;
  Tidal config stays.
- **`api_status.go`** rewired: one probe for the engine `/health`, plus Tidal.
- **New (small):** an engine client (Go) + the shim (Python). That's the only genuinely
  new API surface, and it's internal (compose network only).

### Q4 — Logs?
Keep the whole log system; **add a bridge.** The engine (Python) logs to its own stdout,
invisible to Debug Logs today. The shim captures the module's per-job output and returns
it (or streams it) to our Go, which feeds it into `serverLogs` (`logbuffer.go`/`applog.go`)
→ the `server_log` SSE event. Net effect: engine downloads show up in the Debug Logs page
exactly like Go downloads do now. The engine container also keeps its own `docker logs` for
deep debugging.

### Q5 — Settings passed correctly?
Settings split three ways:
- **Translate → engine request:** provider order (`autoOrder` → engine `services` priority),
  quality (`LOSSLESS`/`HI_RES` → engine `quality`). A thin translation layer, same spirit
  as today's `TidalQualityFor`/`QobuzQualityFor`.
- **Stay ours, applied at ingestion:** filename format, `embedGenre`, lyrics, cover
  policy. These are tagging/naming decisions we keep (engine tagging is off).
- **Obsolete:** Qobuz/Amazon/Deezer proxy lists. **Kept:** Tidal account/token + Tidal
  proxies (BYOT).

So no setting is "lost" — each either gets translated into an engine parameter, applied by
us at ingestion, or retired because the engine owns that concern now.

### Q6 — Spotify-link resolution: keep or delegate? → **DELEGATE.**
The engine takes a Spotify URL and resolves it to the provider track internally, with the
matching we already judged superior (text search + scoring, no ISRC dependency). So our
`songlink → ISRC → Qobuz/Tidal search` chain is **redundant for downloading**.

Consequence to note: **this obviates Piste 1** (porting Qobuz matching to Go). No need to
fix our resolution — the engine is the resolution now. We still scrape Spotify for
*metadata* (Q2), but that's identity/UI, not provider resolution.

**Verified (client.py + downloader.py):** the engine has **no "download this provider
id" entry point**. Input is always a URL → `TrackMetadata` → providers re-resolve from
that metadata; Qobuz/Deezer URLs aren't even accepted as input; `services` only picks
which download providers try (they still re-match internally). So a separate "unified
matcher → provider links → engine" layer is **infeasible** — the engine ignores
pre-resolved links. Matching is necessarily the engine's for the engine route. We keep
control at the **provider level** (`services` order = fallback) and via the Go **BYOT**
layer for Tidal, not at the pre-resolved-link level. Our Go resolution survives only for
BYOT-Tidal and the dormant Go-native fallback.

---

## New data flow

**Before:**
```
worker → ExecuteDownload → [songlink ISRC] → [per-provider resolve + fetch + tag] → file → history
```

**After:**
```
worker
  ├─ Tidal + valid token → Go Tidal (fetch + our tags)
  └─ else → POST engine /download {spotify_url, services, quality, out=/data/jobs/<id>}
              → engine resolves + fetches → FLAC in per-job dir
  → OUR ingestion: our tags (+ SPOTIFY_ID) · catalog · history · M3U8 · move to library
```

Our Spotify scrape still runs upstream to build the queue/watchlist and name files; the
engine does its *own* scrape internally for resolution (a small, acceptable duplication).

---

## What disappears from dev2 (detailed removal map)

Removal is staged (see [Sequencing](#sequencing--safety)) — this is the full target
end-state, not a day-one delete list.

### A. Provider downloaders — after each is prod-proven via the engine
- `backend/qobuz/`, `backend/amazon/`, `backend/deezer/` (+ their `params.go`)
- The `amazon` / `qobuz` / `deezer` cases in `backend/downloader.go`'s `switch`
- **Kept:** `backend/tidal/` (the BYOT path)

### B. Resolution / Song.link — the big cut
`backend/songlink/` is woven through dev2's resolution. Consumers to unwire:
- `api_status.go` (Song.link health probe)
- `app_core.go` (`GetAllURLsFromSpotify` cross-platform URLs)
- `jobs.go` (`songLinkClient` field) + `jobs_helpers.go` (enrichment: Deezer fallback,
  `GetAllURLsFromSpotify`, `ScrapeSongLinkViaAppleMusic`, `ScrapeSongLinkHTML`)
- `backend/downloader.go` (the `isrcChan` chain)
- `backend/qobuz/client.go` (`GetISRC` + `searchByISRC`)
- `backend/amazon/client.go` (`GetAllURLsFromSpotify` for Amazon resolution)

**Reason:** the engine resolves internally.

**ISRC source (CORRECTED — my earlier "Spotify has no ISRC" was wrong; verified live on dev2):**
dev2 already ships `spotify.GetTrackISRC` (`backend/spotify/identifiers.go`), which fetches the
ISRC **authoritatively from Spotify's own metadata microservice**
(`spclient.wg.spotify.com/metadata/4/track/…`, same anonymous token as `Query()`, shape-validated).
Tested live: Mohawk → `DEG739118443`, bad guy → `USUM71900764`. My earlier "no ISRC" grep ran on
the *worktree* branch, which lacks this file — dev2 has it.

This settles it on **option (c)**: BYOT-Tidal calls `GetTrackISRC(spotifyID)` → Tidal
`api.tidal.com/v1/tracks?isrc=` for an **exact** match — **no Song.link, no Deezer fallback** for
ISRC. (The dev2 author built it precisely because name-based Song.link/Deezer matching hits the
wrong edition/remaster.)
- **New call to add:** Tidal currently resolves by *name* (`SearchTidalByName`); exact ISRC lookup
  (`/v1/tracks?isrc=`) is a new call for the BYOT path.
- **Graceful fallback:** `GetTrackISRC` can 404 / return `no ISRC` for some ids → fall back to
  Tidal name-search, then the engine.
- **Bonus:** UPC is on the same metadata service (one extra round-trip), currently unused —
  available if we ever want UPC-based album matching.

So Song.link is **fully removable for ISRC**; its cross-platform-URL role still goes via the engine.

### C. Community / Selenium — after ALL delegated providers are prod-proven
- `backend/community/` (solver / refresh / verify / endpoints / sign / session / client)
- The community wiring in `main.go` (`InitStore`, `SolverFromEnv`, `RefreshLoop`, `AppVersion`)
- The `turnstile-solver` compose service + `TURNSTILE_SOLVER_URL`

### D. Proxy config — the non-Tidal parts
- `backend/util/proxy_config.go`: `amazonProxies`, `deezerProxies`, `qobuzProviders`,
  `qobuzMusicDLURL` (+ getters/setters)
- `api_proxies.go`: the Qobuz/Amazon/Deezer proxy-config endpoints (and their UI)
- **Kept:** `tidalProxies` + `proxy_discovery.go` (tidal-uptime) — the token rides on them (BYOT)

### E. External status probes (`api_status.go`)
- Remove: musicdl.me, spotbye, deezmate, Qobuz GET providers, Song.link probes
- Add: engine `/health` · Keep: Tidal probe

### F. Quality mapping (moves, not deleted)
- `QobuzQualityFor` → folds into the settings→engine translation layer
- `TidalQualityFor` → stays (BYOT)

### G. Files gutted (kept, but heavily cut — not deleted)
- ✂️ `backend/downloader.go` — the `amazon`/`qobuz`/`deezer` (and `auto`) `switch` cases go;
  the `isrcChan` Song.link chain goes (Tidal ISRC now via `GetTrackISRC`); collapses to
  **Tidal(BYOT) + engine client**. `QobuzQualityFor` ➡️ moves into the settings→engine translation.
- ✂️ `app_core.go` — the `GetAllURLsFromSpotify` job resolution/enrichment goes (engine resolves).
- 🔻 `jobs_helpers.go` — Song.link enrichment (`GetDeezerSearchFallback`, `GetAllURLsFromSpotify`,
  `ScrapeSongLinkViaAppleMusic`, `ScrapeSongLinkHTML`) goes.
- 🔻 `jobs.go` — the `songLinkClient` field goes.
- 🔻 `api_status.go` — musicdl/spotbye/deezmate/Qobuz-GET/Song.link probes go; **+ engine `/health`**; Tidal kept.
- 🔻 `api_proxies.go` — Qobuz/Amazon/Deezer proxy endpoints go; Tidal kept.
- 🔻 `backend/util/proxy_config.go` — `amazonProxies`/`deezerProxies`/`qobuzProviders`/`qobuzMusicDLURL`
  (+ getters/setters/defaults) go; `tidalProxies` + discovery kept.
- 🔻 `main.go` — community wiring goes (`InitStore`, `SolverFromEnv`, `RefreshLoop`, `AppVersion`).

### H. Frontend
- 🔻 `components/settings/ApisTab.tsx` — the Qobuz/Amazon/Deezer proxy-config UI goes; Tidal config
  + a new engine-health row stay.
- `lib/downloadFallback.ts` — client-side cross-provider fallback → **redundant** (engine owns fallback).
  *(confirm exact role before deleting.)*
- 🔻 `lib/settings.ts` — obsolete: the proxy lists; `autoOrder` changes meaning (→ engine `services` order).
- ✅ Unchanged: `useDownload.ts`, `rpc.ts`, `SettingsPage.tsx`, `GeneralTab.tsx`, `MaintenanceTab.tsx`,
  `WatchlistPage.tsx`.

### I. Infra / compose / docs
- 🗑️ `turnstile-solver` compose service + `TURNSTILE_SOLVER_URL` env.
- 🗑️ the Selenium / headless-browser dependency it carried.
- 🔻 `docs/EXTERNAL_APIS.md` — the Qobuz/Amazon/Deezer proxy + Song.link sections become historical.

### ⚠️ Three traps — do NOT over-delete
- **`backend/meta/genre_deezer.go` + `genre_apple.go` are KEPT** — *genre* sources for tagging, **not**
  the Deezer/Apple download providers. Deleting "Deezer" removes only `backend/deezer/`.
- **`backend/spotify/identifiers.go` (`GetTrackISRC`) is KEPT and becomes load-bearing** — the new ISRC
  source (BYOT-Tidal + genre). Must not be swept up with the Song.link removal.
- **`ratelimit.go` is KEPT** — the per-IP *login* rate-limiter, unrelated to Song.link's rate-limit guard.

### Kept — the whole service shell (for contrast)
`backend/tidal/` (BYOT) · `backend/spotify/` (metadata + `GetTrackISRC`) · `backend/meta/` (tagging,
genre incl. `genre_deezer`/`genre_apple`, lyrics, M3U8, cover) · `backend/db/` (catalog) ·
`backend/audio/` · `backend/providerutil/` (atomic write, `genremeta` — ISRC now from `GetTrackISRC`) ·
`backend/util/` (minus proxy config) · `backend/history.go` · `backend/filemanager.go` ·
`backend/uploader.go` · `proxy_discovery.go` (tidal-uptime, for BYOT) · `ratelimit.go` · jobs* ·
watcher* · `sse.go` · `auth.go` · `logbuffer.go` · `applog.go` · the `api_*` surface (minus the trimmed
proxy/status parts) · `container.go` · the service files.

---

## Sequencing & safety

1. Add engine + shim + Go client (nothing removed).
2. **Deezer pilot** (no DRM) → validate `resolve → FLAC → ingestion → catalog → SSE`.
3. Qobuz through the engine → validate in prod.
4. **Only then** delete the dead downloaders (`backend/{qobuz,amazon,deezer}`), the
   redundant resolution (`backend/songlink` down-scope), and the **Selenium/Turnstile
   solver**.
5. **Never** delete Go-Tidal — it is the permanent BYOT path.

Rollback at any point before step 4 is a config flip (route the provider back to the Go
`switch`), because nothing is deleted until it's proven.
