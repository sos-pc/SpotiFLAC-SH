# Credits & Attributions

SpotiFLAC Web is built on the work of many open-source developers and community contributors.

---

## Original Project

**[afkarxyz/SpotiFLAC](https://github.com/afkarxyz/SpotiFLAC)**
The original desktop application that SpotiFLAC Web is based on. The core download logic (Tidal, Qobuz, Amazon, Deezer, Spotify metadata, lyrics) was originally developed by afkarxyz and is the foundation of this project.

---

## Community Proxies & Hosted Services

These community-maintained services provide the "zero-account" FLAC streaming that powers SpotiFLAC's default mode.

**Tidal HiFi Proxies**
- **triton.squid.wtf** — `https://triton.squid.wtf`
- **api.monochrome.tf** — `https://api.monochrome.tf` (+ ohio-1, singapore-1 nodes)
- **qqdl.site** — wolf, maus, vogel, katze, hund nodes
- **spotisaver.net** — `https://hifi-one.spotisaver.net` / `hifi-two.spotisaver.net`

**Self-hostable Tidal Proxy**
- **[binimum/hifi-api](https://github.com/binimum/hifi-api)** — Fork of [sachinsenal0x64/hifi](https://github.com/sachinsenal0x64/hifi). A self-hostable Python proxy for Tidal supporting `HI_RES_LOSSLESS`, `LOSSLESS`, `HIGH`, `LOW` quality and Dolby Atmos. Compatible with SpotiFLAC's Tidal proxy slot (`Settings → APIs → Proxy Configuration`).

**Qobuz Proxies**
- **afkarxyz** — `https://qbz.afkarxyz.qzz.io`
- **yeet.su** — `https://dab.yeet.su`
- **dabmusic.xyz** — `https://dabmusic.xyz`

**Amazon Music Proxy**
- **afkarxyz** — `https://amzn.afkarxyz.fun`

**Deezer Proxy**
- **deezmate** — `https://api.deezmate.com`

All proxy lists are configurable at runtime in **Settings → APIs → Proxy Configuration**.

---

## Tidal Device Code Credentials

The OAuth 2.0 Device Code flow uses application credentials shared across the community of Tidal client projects:

- `client_id: 4N3n6Q1x95LL5K7p`
- `client_secret: oKOXfJW371cX6xaZ0PyhgGNBdNLlBZd4AKKYougMjik=`

These credentials are sourced from:
- **[orpheusdl-tidal](https://github.com/Dniel97/orpheusdl-tidal)** — Tidal downloader module

These are public application credentials (not tied to any user account). The previous TV client_id (`fX2JxdmntZWK0ixT`) was replaced because it conflicts with the Tidal desktop application's client_id, causing the desktop app to be forcibly disconnected.

---

## Third-Party APIs

| Service | URL | Usage |
|---------|-----|-------|
| **Odesli / Song.link** | https://song.link | Spotify → Tidal/Qobuz/Amazon link resolution |
| **LRCLIB** | https://lrclib.net | Synchronized & unsynchronized lyrics |
| **MusicBrainz** | https://musicbrainz.org | Genre & label metadata |
| **Deezer Public API** | https://api.deezer.com | ISRC resolution fallback |
| **Spotify Web API** | https://open.spotify.com | Track metadata, cover art, previews |

---

## FFmpeg Binaries

Pre-compiled FFmpeg binaries are sourced from:

**[afkarxyz/ffmpeg-binaries](https://github.com/afkarxyz/ffmpeg-binaries)**
Used on first launch to auto-install `ffmpeg` and `ffprobe` on Windows, Linux, and macOS. Not used in the Docker image (FFmpeg is pre-installed).

---

## Go Libraries

| Library | Author | Usage |
|---------|--------|-------|
| [go-flac/go-flac](https://github.com/go-flac/go-flac) | go-flac | FLAC file reading/writing |
| [go-flac/flacvorbis](https://github.com/go-flac/flacvorbis) | go-flac | FLAC Vorbis comment tags |
| [go-flac/flacpicture](https://github.com/go-flac/flacpicture) | go-flac | FLAC embedded artwork |
| [mewkiz/flac](https://github.com/mewkiz/flac) | mewkiz | Alternative FLAC library |
| [bogem/id3v2](https://github.com/bogem/id3v2) | bogem | ID3v2 tag writing |
| [go.etcd.io/bbolt](https://github.com/etcd-io/bbolt) | etcd-io | Embedded key-value database |
| [pquerna/otp](https://github.com/pquerna/otp) | pquerna | TOTP / 2FA support |
| [ulikunitz/xz](https://github.com/ulikunitz/xz) | ulikunitz | XZ decompression (FFmpeg extraction) |

---

## Disclaimer

SpotiFLAC Web is intended for personal use with content you have the right to access. The community proxies listed above are operated by their respective maintainers and are not affiliated with this project. Proxy availability may change over time.
