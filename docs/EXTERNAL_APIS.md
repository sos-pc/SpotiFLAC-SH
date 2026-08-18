# External APIs & Dependencies

> **🌍 Observation — partiellement périmé.** Ce document décrit des services tiers, qui meurent sans
> prévenir : la section Amazon a été corrigée le 2026-07-15 (le proxy ne résout plus), **le reste n'a
> pas été re-vérifié depuis**. Re-tester avant de citer quoi que ce soit d'ici comme un fait.
> Index : [README.md](README.md).
>
> **Ce qui a changé depuis (v4.0.0).** Les fournisseurs Qobuz, Amazon et Deezer ne sont plus
> atteints par notre code : le moteur en sidecar s'en charge, et il résout **ses** endpoints depuis
> un registre chiffré récupéré **à l'exécution**. Aucun document ne peut donc en tenir l'état à jour —
> c'est structurel, pas un oubli. Voir [module-engine.md](module-engine.md) §5 pour la méthode de
> constat, et [archive/third-party-layer-status.md](archive/third-party-layer-status.md) pour le
> relevé de l'ancienne couche, conservé comme base de preuve.

SpotiFLAC relies on a layered ecosystem of official public APIs, undocumented endpoints, and community-hosted services to achieve "zero-account" FLAC downloading.

This document catalogs every external resource used by the backend.

> **No longer configurable.** Community proxy lists for Qobuz, Amazon and Deezer went with their native downloaders (2026-07, items 3–5); the Tidal one and its **Settings → APIs → Proxy Configuration** screen followed on 2026-07-28. Provider routing is decided by `ENGINE_SERVICES` and by whether a Tidal token is present — see [module-engine.md](module-engine.md).

---

## 1. Metadata & link matching (the core)

Before downloading any audio, SpotiFLAC must fetch metadata from Spotify and find the equivalent track on a lossless platform (Tidal, Qobuz, Amazon, Deezer).

### Spotify
Used strictly for metadata (track names, artists, album art, release dates, IDs).

- **`https://api-partner.spotify.com/pathfinder/v2/query`** — Undocumented GraphQL endpoint used by the Spotify Web Player. Authenticated via a TOTP-derived bearer token generated client-side.
- **`https://open.spotify.com/api/token`** — Used to anonymously generate client credentials tokens.
- **`https://i.scdn.co/image/`** — Spotify's CDN for downloading high-resolution cover art.
- **`https://p.scdn.co/mp3-preview/`** — 30-second audio previews.

A second metadata source used to sit behind this one — a SpotFetch-compatible API at a configurable URL, tried whenever the native scraper failed. **It was removed**: the shipped default was an unreachable third party, the fallback never fired, and upstream carries no such setting. See [settings-reference.md](settings-reference.md#retired-keys).

The Spotify track ID is **persisted into every downloaded audio file** as a `SPOTIFY_ID` tag (Vorbis comment / `TXXX` / iTunes atom). This tag is what `meta.BuildSpotifyIDIndex` later uses to regenerate M3U8 playlists straight from the filesystem, independent of BoltDB.

### ~~Odesli (Song.link)~~ — removed 2026-07-28

Song.link used to be the matching engine: Spotify ID → a Tidal/Qobuz/Amazon link,
or an ISRC. **No code calls it any more.** The download engine resolves each
provider internally from the Spotify URL, so cross-platform links stopped being
something we need. Its JSON API, its two HTML-scrape quota paths and the
9-calls-per-minute guard that made them tolerable all went with it.

What survived is an **ISRC resolver**, renamed `backend/isrclookup/` on
2026-07-28 to stop the old name implying a dependency that no longer exists:
`Resolve` (Spotify's own catalog record, cached in BoltDB) with `ResolveByName`
(Deezer's public search, a name match) behind it.

### Deezer (public API)
The ISRC fallback behind `GetISRCDirect`, **and** a download source.

- **`https://api.deezer.com/search`** — Public search endpoint.
- **`https://api.deezer.com/track/{id}`** — Track metadata endpoint.

---

## 2. Audio downloading (the providers)

Two paths, and which one runs is not a preference but a capability question:

| | Path | Why |
|---|---|---|
| **Tidal, token present** | `api.tidal.com` directly | A personal token is the only way to get full FLAC from Tidal. Nothing else can substitute for it. |
| **Everything else** | the download engine sidecar | Qobuz, Amazon and Deezer are engine-only. Tidal without a token is too. |

`ENGINE_SERVICES` lists the providers delegated to the engine; `autoOrder` still
decides which providers are tried and in what order. See
[module-engine.md](module-engine.md) for the contract and the deployment.

### Tidal

The one provider with a native path left, because it carries user credentials.

**OAuth (Device Code flow)** — see [tidal-auth.md](tidal-auth.md) for the full
flow, storage and refresh behaviour.

```
client_id      4N3n6Q1x95LL5K7p        (orpheusdl-tidal credentials)
```

These are public application credentials shared across the community. The previous TV `client_id` (`fX2JxdmntZWK0ixT`) was retired because it conflicts with the official Tidal desktop application's client ID, causing the desktop app to be forcibly disconnected. See [CREDITS.md](../CREDITS.md).

- **`https://api.tidal.com/v1/tracks/{id}/playbackinfopostpaywall`** — the download URL, with `assetpresentation=FULL`. Requires the token.
- **`https://api.tidal.com/v1/search/tracks`** — name search, used when no Tidal URL was resolved from the ISRC.

**~~Community HiFi proxies~~ — removed 2026-07-28.** A list of monochrome-family
hosts used to serve as the tokenless fallback. Every one of them answers
`assetPresentation: "PREVIEW"` (30-second segments) without a personal token, and
the download path refuses previews — so the fallback could only ever exhaust the
list and fail, at up to 5 s per host. Tokenless Tidal goes through the engine.

The same removal took the `tidal-uptime.geeked.wtf` auto-discovery (already dead:
NXDOMAIN), the `PUT /api/v1/apis/proxies` endpoint, and the settings screen.

### ~~Qobuz, Amazon Music, Deezer~~ — native downloaders removed 2026-07

All three were anonymous community-proxy wrappers: one hand-maintained host each,
which is exactly what the engine does with more routes and third-party
maintenance. `backend/{qobuz,amazon,deezer}` (2201 LOC) and `backend/community`
(1837 LOC) were deleted in items 3–5 of
[dead-code-removal-plan.md](archive/dead-code-removal-plan.md).

Endpoint archaeology, kept because the pattern is instructive — these hosts rotate
without announcement, which is the argument for delegating rather than curating:

| Provider | Last known host | Fate |
|---|---|---|
| Qobuz | `dab.yeet.su`, `dabmusic.xyz`, `qbz.afkarxyz.qzz.io` | DNS dead / Cloudflare bot wall / removed upstream |
| Amazon | `amzn.afkarxyz.fun` → `amazon.spotbye.qzz.io` | subdomain removed while the parent still resolves |
| Deezer | `api.deezmate.com/dl/{trackID}` | its only proxy returned HTML |

Amazon tracks were delivered as encrypted `.m4a`. That decryption no longer exists
in this repo — `github.com/Eyevinn/mp4ff` was dropped when the native downloader
went, and DRM handling now lives only in the engine.

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
| Deezer public API | `GET /track/3135556` — parses JSON, flags `error` payloads |
| Apple Music · genre | first tier of the genre chain |
| Download engine | `GET /health` on `ENGINE_URL`. Listed only when configured, so an install without the engine shows no phantom service. This is the probe that says whether delegated providers can run at all. |
| Other (MusicBrainz, LRCLIB, Tidal API, Jellyfin) | `HEAD /` (or `GET /` fallback). 4xx counts as `ok` (server reachable, root path may not exist). 5xx → `down`. 429 → `ratelimited`. |

Status values: `ok`, `down`, `ratelimited`, `unconfigured`.

Gone with the code they probed: the per-proxy Tidal entries (which parsed
`assetPresentation` to flag PREVIEW-only hosts), the Qobuz/Amazon/Deezer proxy
probes, and the Song.link rate-limit override.

## 6. Dependencies & binaries

- ~~GitHub Releases (`afkarxyz/ffmpeg-binaries`)~~ — **removed 2026-08-04.** A first-launch FFmpeg auto-installer inherited from the upstream desktop application, with download URLs for Windows, macOS ARM, macOS Intel and Linux. It had no callers: this project builds `//go:build !wails` and has a single `package main`, so there is no desktop build to install anything for. The repository it pointed at 404s as well. 329 lines and one module dependency (`ulikunitz/xz`) went with it.
- **GitHub Releases (`https://github.com/BtbN/FFmpeg-Builds/releases/...`)** — the web build's actual FFmpeg source: an FFmpeg/FFprobe build with its codec libraries bundled into the executable, fetched and checksum-verified in the Dockerfile's build stage, then copied into the (shell-less) runtime image — deliberately not `apt install ffmpeg`, which on both Debian bookworm and trixie pulls ~30 transitive shared-library dependencies carrying dozens of CVEs this headless audio-only service never exercises. **Not fully static despite the common shorthand:** the binaries still link glibc/libgcc dynamically, which is why the runtime image is `distroless/cc` and not `scratch` — see [ffmpeg-runtime-regression.md](archive/ffmpeg-runtime-regression.md). See [deployment.md](deployment.md#building-from-source).

---

## 7. Removed / retired endpoints

For historical reference:

- **PKCE Web OIDC flow** (`https://login.tidal.com/authorize` callback). Removed in favour of the Device Code flow. Some leftover frontend RPC stubs (`GetTidalAuthURL`, `SubmitTidalCallback`) reference the old endpoints but are dead code.
- **Tidal TV `client_id` `fX2JxdmntZWK0ixT`** — replaced by `4N3n6Q1x95LL5K7p` (orpheusdl-tidal credentials) because the TV ID conflicted with the official Tidal desktop app and forced a desktop logout on every authentication.
- **`api-partner.spotify.com/pathfinder/v1/...`** — replaced by `/v2` in upstream.
- **Yoinkify (`yoinkify.lol/api/`)** — removed in `chore(deezer): remove DownloadFromYoinkify`; the domain went dead.

---

For full attribution of all sources, community tools, and libraries, see [CREDITS.md](../CREDITS.md).
