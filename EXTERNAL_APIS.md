# External APIs & Dependencies

SpotiFLAC relies on a complex ecosystem of official public APIs, undocumented endpoints, and community-hosted proxies to achieve "Zero-Account" FLAC downloading.

This document catalogs every external resource used by the backend.

> **Proxy configuration:** All community proxy lists for Tidal, Qobuz, Amazon and Deezer are editable at runtime via **Settings → APIs → Proxy Configuration** (or `PUT /api/v1/apis/proxies`). Changes apply immediately without restart.

---

## 1. Metadata & Link Matching (The Core)
Before downloading any audio, SpotiFLAC must fetch metadata from Spotify and find the equivalent track on a lossless platform (Tidal, Qobuz, Amazon, Deezer).

### Spotify
Used strictly for gathering metadata (track names, artists, album art, release dates, IDs).
* **`https://api-partner.spotify.com/pathfinder/v2/query`** — Undocumented GraphQL endpoint used by the Spotify Web Player.
* **`https://open.spotify.com/api/token`** — Used to anonymously generate client credentials tokens.
* **`https://i.scdn.co/image/`** — Spotify's CDN for downloading high-resolution cover art.
* **`https://p.scdn.co/mp3-preview/`** — Used to fetch 30-second audio previews.

### Odesli (Song.link)
The primary matching engine used to convert a Spotify ID into a Tidal/Qobuz/Amazon link, or extract the ISRC.
* **`https://api.song.link/v1-alpha.1/links`** — The official JSON API. *Note: Heavily rate-limited (HTTP 429).*
* **`https://song.link/s/{id}`** — HTML fallback. When the JSON API is rate-limited, SpotiFLAC scrapes the `__NEXT_DATA__` blob from the webpage to bypass restrictions.

### Deezer (Public API)
Used as a fallback to resolve ISRC when Song.link fails, and as a download source (see below).
* **`https://api.deezer.com/search`** — Public search endpoint.
* **`https://api.deezer.com/track/`** — Track metadata endpoint.

---

## 2. Audio Downloading (The Providers)

SpotiFLAC tries providers in order: **Tidal → Qobuz → Amazon → Deezer**. Each provider supports a list of proxies with automatic fallback to the next proxy on failure.

### 🌊 Tidal (Primary Provider)

**Official APIs:**
* **`https://login.tidal.com/authorize`** & **`https://auth.tidal.com/v1/oauth2/token`** — PKCE Web OIDC flow (requires a Premium account).
* **`https://api.tidal.com/v1/search/tracks`** — Search tracks by name using a hardcoded public web token.
* **`https://api.tidal.com/v1/tracks?isrc=...`** — Find Tidal tracks by ISRC.
* **`https://api.tidal.com/v1/tracks/{id}/playbackinfopostpaywall`** — Returns the FLAC manifest. *Requires a valid Premium Token (PKCE).*

**Tidal Device Code Credentials:**
The OAuth 2.0 Device Code flow uses `client_id: 4N3n6Q1x95LL5K7p` — sourced from [orpheusdl-tidal](https://github.com/Dniel97/orpheusdl-tidal). The previous TV client_id (`fX2JxdmntZWK0ixT`) conflicted with the Tidal desktop app. See [CREDITS.md](CREDITS.md) for details.

**Community HiFi Proxies (fallback when no personal token):**

> ⚠️ **Status as of May 2026:** All known community proxies are online as servers but return `assetPresentation: "PREVIEW"` (30-second segments) only — Tidal has restricted community tokens to preview access. **Full FLAC downloads require a personal Premium PKCE token** (Settings → Tidal Account). The proxies below are kept so the token flow can use them as the API layer.

* `https://eu-central.monochrome.tf` — v2.10 ✅
* `https://us-west.monochrome.tf` — v2.10 ✅
* `https://hifi-api.kennyy.com.br` — v2.10 ✅
* `https://api.monochrome.tf` — v2.5 ✅
* `https://monochrome-api.samidy.com` — v2.3 ✅
* Self-hosted option: **[binimum/hifi-api](https://github.com/binimum/hifi-api)** — fork of sachinsenal0x64/hifi, runs on port 8000, compatible with the Tidal proxy slot.

### 🟡 Qobuz (Fallback 1)

* **`https://www.qobuz.com/api.json/0.2/track/search`** — Search tracks by ISRC.
* **`https://www.musicdl.me/api/qobuz/download`** — Primary community stream proxy (musicdl.me). Uses POST + `X-Debug-Key` header (AES-256-GCM derived). Added upstream May 2026.
* **`https://dab.yeet.su/api/stream`** — Secondary community stream proxy (⚠️ unreachable as of May 2026).
* **`https://dabmusic.xyz/api/stream`** — Tertiary community stream proxy (⚠️ Cloudflare-protected, inaccessible to API clients as of May 2026).

### 🟠 Amazon Music (Fallback 2)

Amazon tracks are delivered as encrypted `.m4a` files and decrypted via FFmpeg.
* **`https://amazon.spotbye.qzz.io/api/track/`** — Community stream proxy (spotbye). Requires `X-Debug-Key` header (AES-256-GCM derived). Domain updated May 2026 from `amzn.afkarxyz.fun`.

### 🟣 Deezer (Fallback 3)

Deezer downloads are resolved via ISRC lookup on the public Deezer API, then fetched through community proxies.
* **`https://api.deezmate.com/dl/`** — Community stream proxy.

Multiple proxies are supported; SpotiFLAC tries each in order until one succeeds.

> **Note:** Deezer proxy availability depends on community-maintained instances. If the proxy list in the default configuration is outdated, add working instances via **Settings → APIs → Proxy Configuration**.

---

## 3. Lyrics & Tags

* **LRCLIB (`https://lrclib.net/api/`)** — Synchronized (LRC) and unsynchronized lyrics. SpotiFLAC attempts an exact match (`/api/get`) based on track length, with fuzzy search (`/api/search`) as fallback.
* **MusicBrainz (`https://musicbrainz.org/ws/2`)** — Supplementary album/artist metadata (genre, etc.) when `embed_genre` is enabled.

---

## 4. Dependencies & Binaries

* **GitHub Releases (`https://github.com/afkarxyz/ffmpeg-binaries/releases/...`)** — Used on first launch to auto-download the correct `ffmpeg` and `ffprobe` binaries for the host OS (Windows, Linux, macOS) if not already installed. In Docker deployments, FFmpeg is pre-installed and this step is skipped.

---

For full attribution of all sources, community tools, and libraries, see [CREDITS.md](CREDITS.md).
