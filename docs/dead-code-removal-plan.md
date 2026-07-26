# Removing the superseded provider layer — plan

> **🧭 Plan — 2026-07-26, nothing removed yet.** Dependency analysis is measured
> (import graph + LOC), not estimated. Reference for what replaced this code:
> [module-engine.md](module-engine.md).

## 1. The dividing line

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
| **Amazon** | **no observed success** | `no streaming URLs found` / cooldown | ⛔ **not yet** — no evidence either way |
| **Tidal** | anonymous = previews only | personal token = full FLAC | 🔒 **keep permanently** (BYOT) |

⚠️ **Amazon is the trap.** Its native code is as dead-looking as the others, but we
have never seen the engine succeed on it. Deleting it would remove a path without
a proven replacement. It waits for one successful engine download.

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

- **`backend/tidal/`** (1499 LOC) — the BYOT path. Untouched.
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
| `GetAllURLsFromSpotify` | ❌ dies | supplied `amazon_url` (native gone) and `tidal_url` — and Tidal has two better paths of its own: its ISRC endpoint and name search |
| `ScrapeSongLinkHTML`, `ScrapeSongLinkViaAppleMusic` | ❌ die | fallbacks for the above |
| `GetISRC` (via Song.link) | ❌ dies | `genremeta.go:104` calls it where `GetISRCDirect` is strictly better — a leftover |
| `GetDeezerSearchFallback` | ❓ decide | name→ISRC via Deezer's API; only earns its place if `GetTrackISRC` proves insufficient |
| `tidal.GetTidalURLFromSpotify` | ❌ dies | its only caller is `tidal/client.go:766`, and it exists only to consume Song.link URLs |

**Why Song.link is superseded — the accurate reason.** Not because it is down: the
API answered HTTP 200 on 2026-07-26. Because **we no longer need cross-platform
links at all** — the engine resolves internally from the Spotify URL. (The archived
investigation also found it never returns Qobuz links.)

After the cut, what remains is an ISRC provider with a cache and nothing to do with
Song.link. **Rename/move it** (e.g. into `backend/spotify` or a small `isrc`
package), or the misleading name will cause this same mistake again.

### Other code I classified without measuring

Same error class, found by probing instead of reading imports:

| Item | I said | Measured 2026-07-26 |
|---|---|---|
| `proxy_discovery.go` + `tidal-uptime.geeked.wtf` | "keep — BYOT rides on it" | **feed is DNS-dead.** A goroutine wakes every 6 h to fail, plus BoltDB persistence and a 3-tier merge in `GetTidalProxiesEffective` that now merges nothing. Prod logs have shown `no such host` for days. |
| SpotFetch (`spotify.afkarxyz.fun`) | never examined | **unreachable.** A silent fallback for the Spotify scraper, still wired through a setting, a code path and a status probe. |
| Tidal default proxy list | "keep" | `hifi-api.kennyy.com.br` unreachable; the two monochrome hosts answer 200. List is partly stale. |
| `qobuzProviders` | listed for removal | already an **empty slice** — its getters/setters/API/UI manage nothing. |
| MusicBrainz | assumed fine | answered **503** on one probe. One sample is not a verdict, and genre also comes from `genre_apple.go`/`genre_deezer.go` — worth watching, not concluding. |

None of these block the provider removal; they are separate, smaller cleanups. They
are recorded here because the reflex that missed them is the one this plan must not
repeat: **judge code by whether the thing it talks to still answers, not by how many
files import it.**

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
2. **Remove the native fallback call** in `runService` for engine-delegated
   providers. Cheap, reversible, and stops paying latency for a path that has
   never produced a file. *Do this first — it is the measurement that justifies
   the rest.*
3. **Delete Qobuz, then Deezer** — one commit each: package + its `runService`
   case + its params + its proxy config + its status probe + its UI.
4. **Delete Amazon** — same shape, gated on step 1.
5. **Delete `backend/community/`** + the `main.go` wiring + the health probe,
   once no provider references it.
6. **Re-verify**: a full watchlist sync, an explicit download per remaining
   provider, and Tidal with its token (the BYOT path must be untouched).

## 6. Open questions

- **Future BYOT for other providers**: the engine accepts `qobuz_token`,
  `qobuz_local_api_url`, `tidal_custom_api`. So a later Qobuz-account feature is
  probably *passing a credential to the engine*, not reviving native code. Worth
  confirming before anyone treats these deletions as blocking that.
- **Tidal's own future**: if the engine can take our token, Tidal native becomes
  a candidate too. Unverified; not part of this plan.
- **Does anything outside the app read the retired settings?** The proxy lists are
  user-editable via the API; removing the endpoints is a breaking API change for
  any external caller.
