# External APIs

> **🌍 Observation — partiellement périmé.** Ce document décrit des services tiers, qui meurent sans
> prévenir : la section Amazon a été corrigée le 2026-07-15 (le proxy ne résout plus), **le reste n'a
> pas été re-vérifié depuis**. Pour l'état réel et daté de chaque service, voir
> [third-party-layer-status.md](third-party-layer-status.md) — et re-tester avant de citer quoi que ce
> soit d'ici comme un fait. Index : [README.md](README.md). & Dependencies

SpotiFLAC relies on a layered ecosystem of official public APIs, undocumented endpoints, and community-hosted proxies to achieve "zero-account" FLAC downloading.

This document catalogs every external resource used by the backend.

> **Configurable.** All community proxy lists for Tidal, Qobuz, Amazon and Deezer are editable at runtime via **Settings → APIs → Proxy Configuration** (or `PUT /api/v1/apis/proxies`). Changes apply immediately without restart. Submitting an empty list resets to factory defaults.

---

## 1. Metadata & link matching (the core)

Before downloading any audio, SpotiFLAC must fetch metadata from Spotify and find the equivalent track on a lossless platform (Tidal, Qobuz, Amazon, Deezer).

### Spotify
Used strictly for metadata (track names, artists, album art, release dates, IDs).

- **`https://api-partner.spotify.com/pathfinder/v2/query`** — Undocumented GraphQL endpoint used by the Spotify Web Player. Authenticated via a TOTP-derived bearer token generated client-side.
- **`https://open.spotify.com/api/token`** — Used to anonymously generate client credentials tokens.
- **`https://i.scdn.co/image/`** — Spotify's CDN for downloading high-resolution cover art.
- **`https://p.scdn.co/mp3-preview/`** — 30-second audio previews.

When the native scraper fails, SpotiFLAC can transparently fall back to a SpotFetch-compatible API if `spotFetchAPIUrl` is set in user settings (default points to `https://spotify.afkarxyz.fun/api`).

The Spotify track ID is **persisted into every downloaded audio file** as a `SPOTIFY_ID` tag (Vorbis comment / `TXXX` / iTunes atom). This tag is what `meta.BuildSpotifyIDIndex` later uses to regenerate M3U8 playlists straight from the filesystem, independent of BoltDB.

### ~~Odesli (Song.link)~~ — removed 2026-07-28

Song.link used to be the matching engine: Spotify ID → a Tidal/Qobuz/Amazon link,
or an ISRC. **No code calls it any more.** The download engine resolves each
provider internally from the Spotify URL, so cross-platform links stopped being
something we need. Its JSON API, its two HTML-scrape quota paths and the
9-calls-per-minute guard that made them tolerable all went with it.

`backend/songlink/` still exists and still carries the misleading name; what is
left inside is an **ISRC resolver**: `GetISRCDirect` (Spotify's own catalog
record, cached in BoltDB) with `GetDeezerSearchFallback` behind it.

### Deezer (public API)
The ISRC fallback behind `GetISRCDirect`, **and** a download source.

- **`https://api.deezer.com/search`** — Public search endpoint.
- **`https://api.deezer.com/track/{id}`** — Track metadata endpoint.

---

## 2. Audio downloading (the providers)

When `downloader = "auto"`, providers are tried in the order configured by `autoOrder` (24 permutations of `tidal`, `qobuz`, `amazon`, `deezer`). Each provider supports a list of community proxies with automatic fallback to the next proxy on failure. All four downloaders accept a `DownloadParams` struct (the previous 24+ positional argument signatures were retired).

### Tidal (primary provider)

**Official APIs**

- **`https://auth.tidal.com/v1/oauth2/device_authorization`** + **`/token`** — OAuth 2.0 Device Code Flow (RFC 8628). Used for personal Premium account authentication. See [tidal-auth.md](tidal-auth.md).
- **`https://api.tidal.com/v1/search/tracks`** — Search tracks by name with a hardcoded public web token.
- **`https://api.tidal.com/v1/tracks?isrc=…`** — Find Tidal tracks by ISRC.
- **`https://api.tidal.com/v1/tracks/{id}/playbackinfopostpaywall`** — Returns the FLAC manifest. *Requires a valid Premium token.*
- **`https://api.tidal.com/v1/sessions`** — Used to fetch the user's `countryCode` after auth.

**Device Code credentials**

The flow uses application credentials sourced from [orpheusdl-tidal](https://github.com/Dniel97/orpheusdl-tidal):

```
client_id     = 4N3n6Q1x95LL5K7p
client_secret = oKOXfJW371cX6xaZ0PyhgGNBdNLlBZd4AKKYougMjik=
```

These are public application credentials shared across the community. The previous TV `client_id` (`fX2JxdmntZWK0ixT`) was retired because it conflicts with the official Tidal desktop application's client ID, causing the desktop app to be forcibly disconnected. See [CREDITS.md](../CREDITS.md).

**Community HiFi proxies (default Tidal list)**

| Host | Version | Probed 2026-07-28 |
|---|---|---|
| `https://eu-central.monochrome.tf` | v2.10 | 200, 0.33 s |
| `https://us-west.monochrome.tf` | v2.10 | 200, 0.42 s |
| `https://api.monochrome.tf` | v2.5 | 200, 0.29 s |
| `https://monochrome-api.samidy.com` | v2.3 | 200, 0.25 s |
| ~~`https://hifi-api.kennyy.com.br`~~ | — | **removed** — DNS resolves, connection times out |

> **Status.** All four answer `assetPresentation: "PREVIEW"` (30-second segments) without a personal Premium token. **Full FLAC downloads require authentication via Settings → Tidal Account.** The proxies remain useful as the API layer that the personal token rides on top of.
>
> **Nothing prunes this list automatically** since the `tidal-uptime.geeked.wtf` client was removed (item 8). Probe by hand when editing it — use a real Tidal track ID, `441821360` is the app's own: `GET /track/?id=441821360&quality=HI_RES_LOSSLESS`.

**~~Auto-discovery — `tidal-uptime.geeked.wtf`~~ — removed 2026-07-28**

A goroutine used to poll that feed every 6 h and reorder the proxy list from it,
persisting the last result in BoltDB. **The domain is NXDOMAIN** and has been for
months, so every run failed and the three-tier merge it fed never had anything to
merge — `GetTidalProxiesEffective()` always returned exactly `GetTidalProxies()`.
Removed along with the `tidal_discovered` / `discovery_checked_at` /
`discovery_source` fields of `GET /api/v1/apis/proxies`.

The proxy list is now purely what the operator configures (Settings → APIs).
If an equivalent feed appears, the place to reintroduce this is a fetch + a
`SetTidalProxies` call — not a second list to merge.

**Self-hosted alternative**

[binimum/hifi-api](https://github.com/binimum/hifi-api) — fork of `sachinsenal0x64/hifi`, a self-hostable Python proxy compatible with the Tidal proxy slot.

### Qobuz (fallback 1)

- **`https://www.qobuz.com/api.json/0.2/track/search`** — Search tracks by ISRC (official, no auth).
- **`https://www.musicdl.me/api/qobuz/download`** — Primary community stream proxy. Uses POST with an `X-Debug-Key` header (AES-256-GCM derived). Added upstream in May 2026 — see `backend/qobuz/client.go` for the request shape.

**Default `qobuz_providers` list (May 2026)**

Empty. The previously bundled GET-based providers are all unreachable:

- `https://dab.yeet.su/api/stream` — DNS dead.
- `https://dabmusic.xyz/api/stream` — Cloudflare bot protection blocks programmatic access.
- `https://qbz.afkarxyz.qzz.io/api/stream` — Removed by upstream.

Add working self-hosted instances via **Settings → APIs → Proxy Configuration**.

### Amazon Music (fallback 2)

> 🔴 **Dead since at least 2026-07-15: `amazon.spotbye.qzz.io` no longer resolves.** Not a
> transient outage — the parent domain `spotbye.qzz.io` still resolves (Cloudflare), the `amazon.`
> subdomain was **removed**. Amazon is currently a dead branch of the download chain: there is no
> endpoint left to reach, so nothing below this line works. Upstream moved its proxy URLs into
> AES-GCM-encrypted config (`community_endpoints.go`, see `upstream-catchup.md` §S1) — recovering an
> Amazon source starts with finding the current endpoint, not with the DRM question (§S4). See
> [third-party-layer-status.md](third-party-layer-status.md).

Amazon tracks are delivered as encrypted `.m4a` files and decrypted via FFmpeg with `-decryption_key`.
(FFmpeg itself is also currently unable to execute in the Docker image — see
[ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md) — so this path is doubly broken.)

- **`https://amazon.spotbye.qzz.io`** — Community stream proxy (spotbye). Requires `X-Debug-Key` header (AES-256-GCM derived). The `/status` endpoint was used for health checks (a `401` response confirmed the server was up and rejecting unauthenticated requests). **The host no longer resolves, so the check now reports `Host not found`.**

> Domain updated May 2026 from `https://amzn.afkarxyz.fun` → `https://amazon.spotbye.qzz.io`.
> That second host is now gone too — the pattern is that these community endpoints rotate and are
> not announced.

### Deezer (fallback 3)

Resolved via ISRC lookup on the public Deezer API, then fetched through community proxies.

- **`https://api.deezmate.com/dl/{trackID}`** — Community stream proxy.

The proxy list supports multiple instances; SpotiFLAC tries each in order until one succeeds.

> Deezer proxy availability depends on community-maintained instances. If the default list is outdated, add working instances via **Settings → APIs → Proxy Configuration**.

---

## 3. Lyrics & tags

- **LRCLIB (`https://lrclib.net/api/`)** — Synchronized (LRC) and unsynchronized lyrics. SpotiFLAC attempts an exact match (`/api/get`) based on track length, with fuzzy search (`/api/search`) as fallback.
- **MusicBrainz (`https://musicbrainz.org/ws/2`)** — Supplementary album/artist metadata (genre, label, etc.) when `embedGenre` is enabled (default on).

Tag reading goes through `util.ReadFFprobeTags` (extracted to deduplicate ffprobe calls across the codebase). The `meta.BuildSpotifyIDIndex` walker uses native readers for FLAC (`go-flac`) and MP3 (`bogem/id3v2`); only M4A files invoke ffprobe.

---

## 4. Authentication

- **Jellyfin (user-configured `JELLYFIN_URL`)** — `POST /Users/AuthenticateByName`. Used for SpotiFLAC user authentication. See [authentication.md](authentication.md).
- **Tidal Auth (see Tidal section above)** — Optional Device Code flow for full FLAC.

No other identity providers are supported.

---

## 5. Health checks

`GET /api/v1/apis/status` runs a parallel health check (cached for 30 s). The probes are deeper than a simple `HEAD /` for services where uptime ≠ functionality:

| Service | Probe |
|---------|-------|
| Tidal proxies | Real `/track/?id=441821360&quality=HI_RES_LOSSLESS` request, parses `assetPresentation` to flag `PREVIEW`-only proxies as `ratelimited` |
| Qobuz GET providers | Real `/api/stream?trackId=20882393&quality=6` request |
| Qobuz musicdl.me | `GET /api/qobuz/download` — Express returns `Cannot GET ...` confirming server up + POST-only route |
| Amazon proxy | `GET /status` — `401` (no `X-Debug-Key`) confirms server is alive |
| Deezer proxies | `GET /dl/3135556` — full request to confirm the download endpoint works |
| Deezer public API | `GET /track/3135556` — parses JSON, flags `error` payloads |
| Spotify (SpotFetch) | `GET /track/7qiZfU4dY1lWllzX7mPBI3` — parses JSON, requires `name` field |
| Other (Song.link, MusicBrainz, LRCLIB, Tidal API, Jellyfin) | `HEAD /` (or `GET /` fallback). 4xx counts as `ok` (server reachable, root path may not exist). 5xx → `down`. 429 → `ratelimited`. |

Status values: `ok`, `down`, `ratelimited`, `unconfigured`.

The Song.link probe also overrides itself with `ratelimited` if the in-memory rate-limit cache says we're banned (independent of the HTTP response).

---

## 6. Dependencies & binaries

- **GitHub Releases (`https://github.com/afkarxyz/ffmpeg-binaries/releases/...`)** — used by the legacy desktop build for first-launch FFmpeg auto-install. **Not used by the web build.**
- **GitHub Releases (`https://github.com/BtbN/FFmpeg-Builds/releases/...`)** — the web build's actual FFmpeg source: an FFmpeg/FFprobe build with its codec libraries bundled into the executable, fetched and checksum-verified in the Dockerfile's build stage, then copied into the (shell-less) runtime image — deliberately not `apt install ffmpeg`, which on both Debian bookworm and trixie pulls ~30 transitive shared-library dependencies carrying dozens of CVEs this headless audio-only service never exercises. **Not fully static despite the common shorthand:** the binaries still link glibc/libgcc dynamically, which is why the runtime image is `distroless/cc` and not `scratch` — see [ffmpeg-runtime-regression.md](ffmpeg-runtime-regression.md). See [deployment.md](deployment.md#building-from-source).

---

## 7. Removed / retired endpoints

For historical reference:

- **PKCE Web OIDC flow** (`https://login.tidal.com/authorize` callback). Removed in favour of the Device Code flow. Some leftover frontend RPC stubs (`GetTidalAuthURL`, `SubmitTidalCallback`) reference the old endpoints but are dead code.
- **Tidal TV `client_id` `fX2JxdmntZWK0ixT`** — replaced by `4N3n6Q1x95LL5K7p` (orpheusdl-tidal credentials) because the TV ID conflicted with the official Tidal desktop app and forced a desktop logout on every authentication.
- **`api-partner.spotify.com/pathfinder/v1/...`** — replaced by `/v2` in upstream.
- **Yoinkify (`yoinkify.lol/api/`)** — removed in `chore(deezer): remove DownloadFromYoinkify`; the domain went dead.

---

For full attribution of all sources, community tools, and libraries, see [CREDITS.md](../CREDITS.md).
