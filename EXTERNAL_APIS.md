# External APIs & Dependencies

SpotiFLAC relies on a complex ecosystem of official public APIs, undocumented endpoints, and community-hosted proxies to achieve "Zero-Account" FLAC downloading. 

This document catalogs every external resource used by the backend.

---

## 1. Metadata & Link Matching (The Core)
Before downloading any audio, SpotiFLAC must fetch metadata from Spotify and find the equivalent track on a Lossless platform (Tidal, Qobuz, Deezer).

### Spotify
Used strictly for gathering metadata (Track names, Artists, Album Arts, Release Dates, and IDs).
* **`https://api-partner.spotify.com/pathfinder/v2/query`** : Undocumented GraphQL endpoint used by the Spotify Web Player. Extracted via web scraping.
* **`https://open.spotify.com/api/token`** : Used to anonymously generate client credentials tokens.
* **`https://i.scdn.co/image/`** : Spotify's CDN for downloading high-resolution cover arts.
* **`https://p.scdn.co/mp3-preview/`** : Used occasionally to fetch 30-second audio previews.

### Odesli (Song.link)
The primary matching engine used to convert a Spotify ID into a Tidal/Qobuz/Amazon link, or extract the ISRC.
* **`https://api.song.link/v1-alpha.1/links`** : The official JSON API. *Note: Heavily rate-limited (HTTP 429).*
* **`https://song.link/s/{id}`** : HTML fallback. When the JSON API is rate-limited, SpotiFLAC scrapes the `__NEXT_DATA__` blob from the webpage to bypass restrictions.

### Deezer (Public API)
Used as a reliable fallback to resolve ISRC (International Standard Recording Code) when Songlink fails.
* **`https://api.deezer.com/search`** : Public search endpoint.
* **`https://api.deezer.com/track/`** : Track metadata endpoint.

---

## 2. Audio Downloading (The Providers)

### 🌊 Tidal (Primary Provider)
SpotiFLAC attempts to download from Tidal first. It uses a mix of official endpoints and community proxies.

**Official APIs:**
* **`https://login.tidal.com/authorize` & `https://auth.tidal.com/v1/oauth2/token`** : Used for the PKCE Web OIDC Authentication flow (requires a Premium account).
* **`https://api.tidal.com/v1/search/tracks`** : Used to search tracks by name. Authenticated using a hardcoded, robust public web token.
* **`https://api.tidal.com/v1/tracks?isrc=...`** : Used to find Tidal tracks by ISRC.
* **`https://api.tidal.com/v1/tracks/{id}/playbackinfopostpaywall`** : The holy grail endpoint that returns the FLAC manifest. *Requires a valid Premium Token (PKCE).*

**Community HiFi Proxies (Fallback):**
If the user does not have a Tidal Premium account (or token expires), SpotiFLAC routes the download request through instances of `hifi-api` (community servers that proxy Tidal downloads using their own shared premium accounts).
* `https://triton.squid.wtf`
* `https://api.monochrome.tf`
* `https://ohio-1.monochrome.tf` / `https://singapore-1.monochrome.tf`
* `https://wolf.qqdl.site` (along with maus, vogel, katze, hund)
* `https://hifi-one.spotisaver.net` / `https://hifi-two.spotisaver.net`

### 🟡 Qobuz (Fallback 1)
If a track is missing from Tidal, SpotiFLAC switches to Qobuz. Since Qobuz DRM is strict, it uses community-hosted API proxies.
* **`https://www.qobuz.com/api.json/0.2/track/search`** : Official endpoint used to search tracks via ISRC.
* **`https://qbz.afkarxyz.fun/api/track/`** : Primary community stream proxy.
* **`https://dab.yeet.su/api/stream`** : Secondary community stream proxy.
* **`https://dabmusic.xyz/api/stream`** : Tertiary community stream proxy.

### 🟠 Amazon Music (Fallback 2)
Similar to Qobuz, handled via a community proxy to bypass DRM.
* **`https://amzn.afkarxyz.fun/api/track/`** : Community stream proxy.

### 🟣 Deezer (Fallback 3 - Currently Inactive)
* **`https://api.deezmate.com/dl/`** : (Dead) Formerly used to download FLACs directly from Deezer's CDN using an ARL.
* **`https://yoinkify.lol/api/deezer/track/`** : (Dead) Alternative Deezer proxy.

---

## 3. Lyrics & Tags

* **LRCLIB (`https://lrclib.net/api/`)** : Used to fetch both synchronized (LRC) and unsynchronized lyrics. SpotiFLAC attempts an exact match (`/api/get`) based on track length, and falls back to a fuzzy search (`/api/search`) if needed.
* **MusicBrainz (`https://musicbrainz.org/ws/2`)** : Used occasionally to fetch supplementary album or artist metadata if Spotify data is lacking.

---

## 4. Dependencies & Binaries

* **GitHub Releases (`https://github.com/afkarxyz/ffmpeg-binaries/releases/...`)** : Used automatically upon first launch to download the correct `ffmpeg` and `ffprobe` binaries for the host OS (Windows, Linux, macOS) if they are not already installed on the system.