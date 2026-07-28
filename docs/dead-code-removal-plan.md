# Removing the superseded provider layer — plan

> **🧭 Plan — 2026-07-26, nothing removed yet.** Dependency analysis is measured
> (import graph + LOC), not estimated. Reference for what replaced this code:
> [module-engine.md](module-engine.md).

## 1. The dividing line — dead code vs redundancy

Two different reasons to delete, both in scope: code whose counterpart no longer
exists (§2–5), and live code duplicating something the codebase already has (§6).

Not "native vs engine". The distinction that matters:

| | Keep | Reason |
|---|---|---|
| **Code carrying user credentials (BYOT)** | ✅ | It provides something the engine's anonymous access cannot — guaranteed full quality. Tidal today; other providers may follow. |
| **Code wrapping anonymous community proxies** | ❌ | Exactly what the engine does, with more routes, upstream-maintained. Ours is one host per provider, hand-maintained. |

BYOT stays as the **per-provider override**: credentials present → our path; absent
→ engine.

## 2. The gate — measured, per provider

Removal is allowed only where the engine is **proven at least as good**:

| Provider | Engine evidence | Native evidence | Verdict |
|---|---|---|---|
| **Qobuz** | ~10 successes, full-length FLAC, 6–30 s | reached 3×, **0 successes** — dies at `searchByISRC` | ✅ removable |
| **Deezer** | 4/4 on an album, 8–15 s each | its only proxy returns HTML; long dead | ✅ removable |
| **Amazon** | ✅ **3 successes 2026-07-28** (`AMAZON · LOSSLESS`, 11–39 s) | `no streaming URLs found` / cooldown | ✅ removable — gate passed |
| **Tidal** | anonymous = previews only | personal token = full FLAC | 🔒 **keep permanently** (BYOT) |

~~⚠️ Amazon is the trap.~~ **Gate passed 2026-07-28**: three engine downloads
succeeded, so all four providers are proven. With the evidence in hand the
provider-by-provider split lost its purpose — splitting would have retouched
`proxy_config`, `api_status` and `downloader.go` three times over — so the cut was
made in one commit **by concern** instead: native downloaders together, then
`backend/community`, then the Song.link half, then `proxy_discovery`.

## 3. Inventory

Import graph (measured): `backend/{qobuz,amazon,deezer}` are imported by
**`backend/downloader.go` only**. No other call site anywhere. That makes the
deletions unusually clean.

### 3.1 Packages deleted whole

| Package | LOC | Notes |
|---|---|---|
| `backend/qobuz/` | 909 (7 files) | incl. `signed_search.go`, `community.go` |
| `backend/deezer/` | 284 (2 files) | |
| `backend/amazon/` | 1008 (5 files) | **held back** until §2's gate passes |

### 3.2 The package that dies with them — `backend/community/` (1837 LOC, 12 files)

Its consumers are `backend/{qobuz,amazon}` plus two entries that exist *only to
serve them*:
- `main.go:124-153` — `InitStore`, `AppVersion`, `SolverFromEnv`, `RefreshLoop`
- `api_status.go:598` — the `QobuzHealthURL` probe

Remove Qobuz + Amazon and nothing references it.

**The solver container stays.** It is now the engine's grant source
(`TURNSTILE_SOLVER_URL` in the engine service), and the two use different
challenge endpoints, so our Go session was never what the engine consumed.
What disappears is the **Go-side** session plumbing, not the solver.

> Consequence to accept: the `[Community] session valid remaining=…` heartbeat
> goes away. That log line is currently the easiest proof the solver works;
> after removal, the engine's own logs are the only witness.

### 3.3 Files trimmed, not deleted

| File | LOC | What goes |
|---|---|---|
| `backend/downloader.go` | 678 | the `qobuz`/`amazon`/`deezer` cases in `runService`, their `*Params` builders, `QobuzQualityFor` |
| `backend/util/proxy_config.go` | 231 | `qobuzProviders`, `amazonProxies`, `deezerProxies`, `qobuzMusicDLURL` + getters/setters/defaults. **Keep** `tidalProxies` + discovery (BYOT rides on them) |
| `api_status.go` | 656 | probes for musicdl, spotbye, deezmate, Qobuz-GET, community. **Keep** Tidal + Engine |
| `api_proxies.go` | 141 | the three providers' proxy-config endpoints. **Keep** Tidal |
| `main.go` | 232 | the community wiring |
| `frontend/.../ApisTab.tsx` | — | the Qobuz/Amazon/Deezer proxy UI |
| `frontend/src/lib/settings.ts` | — | the proxy lists |

### 3.4 Survives — and why

- **`backend/tidal/`** (1499 LOC) — the BYOT path. Kept, minus `GetTidalURLFromSpotify` (see below).

### `backend/songlink/` — one name, two unrelated jobs

An earlier revision of this plan called it "not deletable" because five files import
it. That judged the import graph instead of the behaviour, and it was wrong.

```go
func (s *SongLinkClient) GetISRCDirect(spotifyTrackID string) (string, error) {
    if cached, err := GetCachedISRC(spotifyTrackID); ...        // BoltDB cache
    isrc, err := s.getSpotifyClient().GetTrackISRC(spotifyTrackID)   // Spotify, direct
```

**`GetISRCDirect` never touches Song.link.** It wraps `spotify.GetTrackISRC` — the
authoritative source — behind a persistent cache. It is the primary ISRC path
(`jobs_helpers.go:65`). The package therefore holds:

| Part | Sort | Why |
|---|---|---|
| `GetISRCDirect` + `isrc_cache.go` | ✅ keep | Spotify-direct + cache. Needed by **BYOT Tidal** (ISRC → Tidal id) and genre lookup — neither goes through the engine. |
| `GetAllURLsFromSpotify` | ❌ dies — **7b** | supplied `amazon_url` (native gone) and `tidal_url` — and Tidal has two better paths of its own: its ISRC endpoint and name search. Last caller is `metadata_service.go:63`, i.e. the availability feature. |
| `ScrapeSongLinkHTML`, `ScrapeSongLinkViaAppleMusic` | ✅ **dead 7a** | fallbacks for the above, reachable only from the deleted cascade |
| `GetISRC` (via Song.link) | ✅ **dead 7a** | `genremeta.go` moved to `GetISRCDirect` in item 12, leaving it callerless |
| `GetDeezerSearchFallback` | ✅ **keep** (decided) | name→ISRC **and `tidal_url`** via Deezer's own API — one call, no rate-limit. It is what replaced the cascade for the BYOT path, not a peer of it. |
| `tidal.GetTidalURLFromSpotify` | ✅ **dead 7a** | its only caller was `tidal/client.go:766`, and it existed only to consume Song.link URLs |

**Why Song.link is superseded — the accurate reason.** Not because it is down: the
API answered HTTP 200 on 2026-07-26. Because **we no longer need cross-platform
links at all** — the engine resolves internally from the Spotify URL. (The archived
investigation also found it never returns Qobuz links.)

After the cut, what remains is an ISRC provider with a cache and nothing to do with
Song.link. **Rename/move it** (e.g. into `backend/spotify` or a small `isrc`
package), or the misleading name will cause this same mistake again. Deferred to
**after 7b**: renaming now would churn `metadata_service.go` and `api_status.go`,
which 7b edits anyway.

### Other code I classified without measuring

Same error class, found by probing instead of reading imports:

| Item | I said | Measured 2026-07-26 |
|---|---|---|
| `proxy_discovery.go` + `tidal-uptime.geeked.wtf` | "keep — BYOT rides on it" | **feed is DNS-dead.** A goroutine wakes every 6 h to fail, plus BoltDB persistence and a 3-tier merge in `GetTidalProxiesEffective` that now merges nothing. Prod logs have shown `no such host` for days. |
| SpotFetch (`spotify.afkarxyz.fun`) | never examined | **unreachable.** A silent fallback for the Spotify scraper, still wired through a setting, a code path and a status probe. |
| Tidal default proxy list | "keep" | `hifi-api.kennyy.com.br` unreachable; the two monochrome hosts answer 200. List is partly stale. |
| `qobuzProviders` | listed for removal | already an **empty slice** — its getters/setters/API/UI manage nothing. |
| MusicBrainz | assumed fine | answered **503** on one probe. One sample is not a verdict, and genre also comes from `genre_apple.go`/`genre_deezer.go` — worth watching, not concluding. |

None of these block the provider removal; they are separate, smaller cleanups,
tracked as 🔍 **to examine** in §5 (items 7–11) with the question each one needs
answered first. They are recorded because the reflex that missed them is the one
this plan must not repeat: **judge code by whether the thing it talks to still
answers, not by how many files import it.**

**Scale:** ~2200 LOC deleted now (Qobuz + Deezer + community), ~1000 more when
Amazon's gate passes, plus trims across 7 files.

## 4. The safety net we are giving up

Today's rollback is: empty `ENGINE_SERVICES` → the native path takes over. **That
disappears** for any provider whose native code is deleted. After this work,
rollback for Qobuz/Deezer means reverting a commit and redeploying.

Mitigations:
1. **Delete per provider, in separate commits** — so a revert is surgical.
2. **Do it after the merge to `dev2`**, not before: `dev2` then holds a tagged
   state where both paths exist, reachable by image tag.
3. Keep the removal commits free of unrelated changes.

## 5. Sequence

1. **Prove Amazon through the engine** (one successful download) — or accept it
   stays native indefinitely. Blocks step 4 only.
2. ✅ **DONE 2026-07-28, then CORRECTED the same day.** Removed the blanket native fallback,
   but that broke BYOT Tidal: the engine's Tidal is tokenless and answers
   `proxy HTTP 401 / no Tidal APIs configured`, while our path refreshes the personal
   token and succeeds on the same track (observed in prod). The "0 successes in 3
   invocations" that justified the removal were **all Qobuz** — generalising them to
   every provider was the same one-observation mistake this document keeps recording.
   `runService` now implements the BYOT override the plan described from §1 but that
   was never actually built: **credentials → native first, engine as backup**; no
   credentials → engine owns it. Removed the native fallback call in `runService` for engine-delegated
   providers. Cheap, reversible, and stops paying latency for a path that has
   never produced a file. *Do this first — it is the measurement that justifies
   the rest.*
3. **Delete Qobuz, then Deezer** — one commit each: package + its `runService`
   case + its params + its proxy config + its status probe + its UI.
4. **Delete Amazon** — same shape, gated on step 1.
5. ✅ **DONE 2026-07-28.** **Deleted `backend/community/`** + the `main.go` wiring + the health probe,
   once no provider references it.
6. **Re-verify**: a full watchlist sync, an explicit download per remaining
   provider, and Tidal with its token (the BYOT path must be untouched).

### Independent items — examined 2026-07-26

Each question below was answered from the code and from live probes, not from the
import graph. Verdicts differ per item; two turned out to be non-issues.

7. **Split `backend/songlink`** — ✅ **7a DONE 2026-07-28**, 7b pending
   Split on execution, because the two halves are not the same size: **7a** removes
   Song.link from the *download* path (backend only, no UI impact); **7b** removes the
   *availability* feature, which the inventory said was 2 frontend files and is
   actually 6 (see §7.4). 7a shipped:

   | removed | LOC | why it was safe |
   |---|---|---|
   | `jobs_helpers.go:getStreamingURLsViaSonglink` + both call sites | ~70 | the chain's only surviving consumer is Tidal-BYOT, which the Deezer step already serves |
   | `jobs.go:songLinkSem` / `songLinkDelay` | 3 | rate-limiter with nothing left to limit |
   | `tidal.GetTidalURLFromSpotify` + the fallback in `Download()` | ~30 | tier 3 of 3 (see below) |
   | `songlink.GetISRC`, `GetDeezerURLFromSpotify` | ~90 | `GetISRC`'s last caller (`genremeta.go`) moved to `GetISRCDirect` in item 12; `GetDeezerURLFromSpotify` existed only to feed it |
   | `songlink.ScrapeSongLinkHTML`, `ScrapeSongLinkViaAppleMusic`, `searchITunes`, `itunesResult` | ~350 | cascade steps 3 and 4, reachable only from the deleted `getStreamingURLsViaSonglink` |
   | `songlink/client_test.go` | 198 | 8 tests, all of `searchITunes` — they died with their subject, not with a behaviour |

   Net: `client.go` 967 → 526 lines, and **`backend/tidal` no longer imports
   `backend/songlink`** — the dependency edge is gone, not just the calls. What stays
   until 7b: `GetAllURLsFromSpotify`, `CheckTrackAvailability`, `checkQobuzAvailability`
   and the rate-limit machinery, all reachable only from `metadata_service.go`.
   The package rename waits for 7b too — renaming now would touch files 7b deletes.

   Tidal's Song.link path was **third-tier**, not primary:
   ```
   1. jobs_helpers.go:278  tidal.GetTidalIDFromISRC   → Tidal's own official API, by ISRC
   2. tidal/client.go:759  SearchTidalByName          → direct search, ~200 ms, no rate-limit
   3. tidal/client.go:766  GetTidalURLFromSpotify     → Song.link, only if 1 AND 2 failed
   ```
   `downloader.go:252` (`ensureTidalServiceURL`) also pre-resolves by name, so most
   downloads take `DownloadByURLWithFallback` and never reach `Download()` — the only
   function that touched Song.link at all. Removing it costs the residual case where
   both the ISRC lookup and the name search fail, i.e. where the track is probably not
   on Tidal. `GetISRCDirect` + `isrc_cache.go` kept.

   **Also done in the same commit** (operator request): Deezer was still greyed out in
   the settings provider selector (`GeneralTab.tsx`, `disabled` + "(unavailable)")
   while working in prod through the engine — 4/4 on an album test. Re-enabled. That
   file was the only place the UI blocked it.

8. **`proxy_discovery.go` + `tidal-uptime.geeked.wtf`** — ✅ **go (contributes nothing)**
   **305 LOC** plus a goroutine and a BoltDB blob. The feed is DNS-dead, and
   `GetTidalProxiesEffective()` explicitly falls back to the static list when there is
   no discovery data — which is now always. Prod confirms it never even restores:
   `[Discovery] Cached result is stale, skipping restore age=70h32m0s`.
   Consumers (`api_status.go:568`, `tidal/client.go:239`) would behave identically off
   the static list. No replacement feed known; if one appears, repoint instead.

9. **SpotFetch** — ❌ **non-issue. Leave it.**
   ⚠️ *An earlier revision of this item called it a "hard switch" that "fails outright",
   from reading `spotify/api.go` without its caller. Wrong* — `metadata_service.go:163`
   runs the **native scraper first** and only falls back when it errors:
   ```go
   data, nativeErr := spotify.GetFilteredSpotifyData(...)   // native first
   if nativeErr == nil { return ... }
   if spotFetchAPIURL != "" { ... GetSpotifyDataWithAPI(..., true, ...) }
   ```
   The hardcoded `useAPI=true` is reached only inside that fallback branch. The host is
   unreachable and prod has the default URL set, so the fallback cannot help — but it
   cannot hurt either, and its failure message reports both errors, which is good
   diagnostics. Removing it buys nothing; repointing it at a live mirror would.

10. **Tidal default proxy list** — 🔍 **still open, but not urgent**
    `hifi-api.kennyy.com.br` unreachable; both monochrome hosts answer 200, and they
    are what the personal token rides on. It works today. With item 8's discovery gone,
    nothing prunes it — the real question is whether a hand-curated list is viable, or
    whether tokenless Tidal should lean on the engine. Drop the dead host meanwhile.

11. **MusicBrainz** — ✅ **no action, confirmed**
    `genre.go` documents a three-tier chain: **Apple Music** (per-track, curated — "the
    precise answer") → **Deezer** (per-album) → **MusicBrainz** (free-form folksonomy).
    MusicBrainz is last. Prod files carry Apple-style genres (`Jazz`,
    `Electronic, Hip-Hop/Rap, Rap, Dance, House`), so the 503 changes nothing.

### Redundancy items — detail in §6

Independent of the provider cut. 12 and 13 are the cheapest changes in this
document and can be done at any time.

12. **Deduplicate `buildCoverFilename` / `buildLyricsFilename`** — ✅ **DONE** (see §6.1)
    Byte-identical except the function name (§6.1). Have one call the other, or extract
    a shared helper in `backend/meta/`. ~33 lines.
    *No behaviour change*, so it needs no measurement — verify by test, not in prod.

13. **Point `genremeta.go` at `GetISRCDirect`** — ✅ **DONE 2026-07-26.** `GetISRC` now has one caller left (`backend/qobuz`), which goes at item 3.
    It currently calls `songlink.GetISRC` (via Song.link) where the rest of the app uses
    the Spotify-direct + cached path (§6.2). After this, `GetISRC` has no callers and
    goes with the Song.link half in item 7.
    *Side benefit:* genre lookups start hitting the ISRC cache instead of a third-party
    aggregator, so they get faster and stop depending on Song.link being up.

14. **Close the `buildTidalFilename` divergence** — ⚠️ **before any template change**
    `util.BuildExpectedFilename` substitutes `{playlist}`/`{creator}`;
    `buildTidalFilename` never does (§6.1). The on-disk check uses the first, the Tidal
    download the second — so a template using either placeholder re-downloads the track
    on every pass.
    Latent today (`{title} - {artist}`), which is exactly why it is easy to forget.
    Fix by making Tidal call the canonical builder, and add a test asserting the two
    agree for a template containing `{playlist}`.
    **Ordering: do this before touching `filenameTemplate`, not after.**

15. **Collapse the Tidal `DownloadParams` translation** — 🔍 judgment call
    Three of the four structs die with their providers (§6.3), leaving one translation
    layer for a single consumer. Whether to pass `DownloadRequest` into
    `backend/tidal` directly is a taste question about package coupling, not a
    correctness one. Decide when items 3–5 are done and the shape is visible.

## 6. Redundancy — live code, duplicated

Not dead code: every line below runs. It is duplication of something that already
exists elsewhere, which makes it removable for a different reason — and in one case
the two copies **disagree**, which is worse than either.

### 6.1 Five filename builders, two of which diverge

| Builder | Fate |
|---|---|
| `util.BuildExpectedFilename` | canonical — used for the already-on-disk check and by the engine ingestion |
| `buildQobuzFilename` (`qobuz/client.go:300`) | dies with the package |
| `buildTidalFilename` (`tidal/client.go:947`) | **survives the provider cut** |
| `buildCoverFilename` (`meta/cover.go:68`) | duplicate — see below |
| `buildLyricsFilename` (`meta/lyrics.go:365`) | duplicate — see below |

**`buildCoverFilename` and `buildLyricsFilename`** — ✅ **DONE 2026-07-26**, merged into
`backend/meta/sidecar_filename.go`.

⚠️ *An earlier revision called them "byte-identical except the function name". Wrong:
that diff covered only the first 33 lines of a 56- and a 54-line function.* The full
diff showed three differences, and one is behavioural:

| Difference | Verdict |
|---|---|
| `case "title-artist"` present in cover, absent in lyrics | **harmless** — lyrics' `default:` produces exactly the same string (asserted by test) |
| track-number separator: `"%02d - %s"` vs `"%02d. %s"` | ⚠️ **real divergence** — `01 - Title.jpg` vs `01. Title.lrc` |
| `.jpg` vs `.lrc` | legitimate |

So this was **not** the trivial win advertised. Merging naively would have renamed every
sidecar already on disk. Both differing details are parameters instead, output unchanged
for either caller, with tests pinning the separators so a later "harmonisation" has to be
a deliberate migration rather than a tidy-up.

Left open: lyrics number as `01. ` (matching the audio file) while cover uses `01 - `.
Nobody chose that; it is inconsistency, not design. Harmonising means renaming existing
covers — a migration question, out of scope here.

**⚠️ `BuildExpectedFilename` and `buildTidalFilename` are NOT equivalent:**

| | `BuildExpectedFilename` | `buildTidalFilename` |
|---|---|---|
| `{playlist}` / `{creator}` | substituted | **never replaced** |
| sanitising | internal | expected of the caller |
| track number | receives it resolved | resolves it itself |

The already-on-disk check uses the first; the native Tidal download uses the second.
With a template containing `{playlist}`, the check would look for a substituted name
while the download writes one with the literal `{playlist}` left in — so the file
would be re-downloaded on every pass. Exactly the class of bug the comment inside
`buildTidalFilename` already documents for track numbers.

**Latent, not live:** prod's `filenameTemplate` is `{title} - {artist}`, which uses
neither placeholder. It springs the moment someone adds one.

### 6.2 Four ways to obtain an ISRC

| Path | Nature |
|---|---|
| `spotify.GetTrackISRC` | authoritative — Spotify's own metadata service |
| `songlink.GetISRCDirect` | the above **plus a BoltDB cache** — the one to keep |
| `songlink.GetISRC` | via Song.link — strictly worse, and Song.link has no Qobuz coverage |
| `songlink.GetDeezerSearchFallback` | name→ISRC via Deezer's API |

`jobs_helpers.go:65` uses `GetISRCDirect`; `providerutil/genremeta.go:104` uses
`GetISRC`. **Same goal, two sources, the worse one still wired.** Point genremeta at
`GetISRCDirect` and `GetISRC` loses its last caller.

### 6.3 Four `DownloadParams` structs translating one `DownloadRequest`

`backend/{tidal,qobuz,amazon,deezer}/params.go` — 45/39/40/38 lines of near-identical
fields, each filled by its own builder closure in `downloader.go`. Three die with
their providers; **the Tidal one remains as a translation layer for a single
consumer**, at which point passing `DownloadRequest` directly is worth considering.

### 6.4 Not redundant, despite appearances

Checked and cleared, so nobody removes them later on a hunch:
`TidalQualityFor` / `QobuzQualityFor` (canonical → provider code),
`deriveCatalogQuality` / `actualCatalogQuality` (intended vs measured quality),
`defaultQualityForExt` (admin retag). Different questions, not copies.

### Sequencing

Scheduled as items **12–15** in §5. The cover/lyrics twins and the genremeta
repointing are the cheapest changes here; the `buildTidalFilename` divergence carries
an ordering constraint (close it **before** touching the filename template).

## 7. What the inventory missed

§3 claimed `backend/{qobuz,amazon,deezer}` are "imported by `downloader.go` only". True
for **package imports**, false for **provider logic** — a second sweep found it spread
much wider. Recorded because each item changes the size or the risk of the work.

### 7.1 The enrichment chain runs for engine downloads, and its output is discarded — ✅ **DONE 2026-07-26**

`jobs_helpers.go:getStreamingURLs` runs a cascade **per job** — Deezer API → Song.link →
Apple Music scrape → HTML scrape — producing `tidal_url`, `amazon_url`, `isrc`.

A delegated provider needs **only the Spotify URL**. Of that cascade the engine path
consumes just `isrc` (for genre); the URLs go nowhere. So every delegated download pays
several third-party round-trips for a result largely thrown away.

**This is a per-download cost, not dead lines** — arguably the most valuable item in this
document, and it needs no deletion: skip the URL resolution when the target provider is
engine-delegated.

### 7.2 Provider logic outside `downloader.go`

| Location | What it does |
|---|---|
| `jobs_helpers.go:36` | `if s.Service == "deezer" { return nil }` — skips enrichment entirely |
| `jobs_helpers.go:81` | `if s.Service == "amazon"` — Amazon URLs come only from Song.link |
| `jobs_helpers.go:347` | `resolveAudioFormat` — per-service quality vocabulary |
| `jobs_catalog.go:321` | `deriveCatalogQuality` — per-service → catalog quality label |
| `backend/meta/genre.go:66` | `{"deezer", deezerGenreNames}` — ⚠️ **genre source, keep** (see §6.4's warning) |

### 7.3 Our per-provider quality vocabulary leaks into the engine call

`resolveAudioFormat` returns `"6"` for Qobuz and `"flac"` for Deezer; `downloadViaEngine`
forwards that verbatim as the engine's `quality`. It happens to work — the engine accepted
`27` and produced hi-res, accepted `flac` and produced "FLAC Best Available" — but that is
**accidental compatibility, not a contract**. The delegated path should send the canonical
vocabulary (`LOSSLESS` / `HI_RES_LOSSLESS`) and let one translation layer own the mapping.

### 7.4 The frontend surface is 8 files, not 2

`ApisTab.tsx`, `GeneralTab.tsx`, `TrackList.tsx`, `TrackInfo.tsx`, `WatchlistPage.tsx`,
`lib/rpc.ts`, `lib/settings.ts`, `types/api.ts`.

**⚠️ And one is a user-visible feature this plan would have silently broken:**
`GET /api/v1/tracks/{id}/availability` returns `songlink.TrackAvailability`, built from
Song.link's `linksByPlatform`; `TrackList.tsx:332` and `TrackInfo.tsx:144` render it as
green/red per-provider dots. Deleting the cross-platform half of Song.link (item 7)
removes its data source.

Bitter detail: the archived investigation found Song.link **never returns Qobuz links**,
so the Qobuz dot is presumably always red — the feature already misinforms for the
provider that now works best.

> **✅ Decided 2026-07-26 (operator):** delete Song.link anyway and let the availability
> dots **break temporarily**. `GET /api/v1/tracks/{id}/availability` is flagged
> **to rework** on a non-Song.link source — the engine resolves per provider, so a real
> availability check is possible and would be *more* accurate than what it replaces.
> Breaking a feature that already lies about Qobuz is an acceptable trade.

### 7.5 Test coverage disappears without being counted

`backend/community` 21 tests, `backend/qobuz` 7. (`amazon` and `deezer` have **none** — a
statement in itself.) The plan counts deleted lines and never counted lost coverage.

### 7.6 `check-upstream.sh` will flag the deletions forever

It **auto-discovers** upstream files (`git ls-tree -r upstream/main -- backend/`) rather
than reading a fixed list. Once our packages are gone it reports them as diverged on every
run. It needs an ignore list, or it cries wolf permanently.

### 7.7 Accepted: the engine becomes a hard dependency

Removing the native fallback means an engine outage fails every delegated provider.
**Confirmed as intended** — this plan makes the fork the core of the app, dependency
included. Recorded so it is a decision, not a discovery.

### 7.8 Smaller consequences

- Catalog rows keep `provider=qobuz` etc. Nothing breaks; the data references a path
  that no longer exists.
- Debug Logs loses diagnostic value: engine logs reach `docker logs`, not our SSE feed,
  so diagnosing a provider failure moves to the second container.
- The frontend Source selector could offer a provider with neither a native nor a
  delegated path → `unknown service`.

### 7.9 Resolved: keep the one-provider-at-a-time call

§5 item 2 removes one of the two reasons for walking the chain ourselves rather than
handing `services: [a,b,c]` to the engine in a single call. The remaining reasons hold:
per-provider log attribution, the per-provider quality retry, and — decisively — **the
user's `autoOrder` drives the order, not the engine's**. No change.

## 8. Open questions

- **Future BYOT for other providers**: the engine accepts `qobuz_token`,
  `qobuz_local_api_url`, `tidal_custom_api`. So a later Qobuz-account feature is
  probably *passing a credential to the engine*, not reviving native code. Worth
  confirming before anyone treats these deletions as blocking that.
- **Tidal's own future**: if the engine can take our token, Tidal native becomes
  a candidate too. Unverified; not part of this plan.
- **Does anything outside the app read the retired settings?** The proxy lists are
  user-editable via the API; removing the endpoints is a breaking API change for
  any external caller.
