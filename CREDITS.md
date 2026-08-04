# Credits & Attributions

SpotiFLAC Web is built on the work of many open-source developers and community contributors.

---

## Original Project

**[spotbye/SpotiFLAC](https://github.com/spotbye/SpotiFLAC)**
The original desktop application that SpotiFLAC Web is based on. The core download logic (Tidal, Qobuz, Amazon, Deezer, Spotify metadata, lyrics) was originally developed by spotbye (formerly afkarxyz) and is the foundation of this project.

---

## Community Proxies & Hosted Services

**None of these are called by this codebase any more**, and the section stays as
attribution rather than documentation. The "zero-account" path they used to
provide is now the download engine's job (see
[module-engine.md](docs/module-engine.md)); the native wrappers around them were
removed through July 2026.

**Tidal HiFi Proxies** — removed 2026-07-28. Every one serves 30-second previews
without a personal Premium token, which the download path refuses.
- **eu-central.monochrome.tf**, **us-west.monochrome.tf**, **api.monochrome.tf**,
  **monochrome-api.samidy.com** — all answering 200 when last probed, 2026-07-28.
- **hifi-api.kennyy.com.br** — stopped answering; dropped a little earlier the same day.

**Self-hostable Tidal Proxy**
- **[binimum/hifi-api](https://github.com/binimum/hifi-api)** — Fork of [sachinsenal0x64/hifi](https://github.com/sachinsenal0x64/hifi). A self-hostable Python proxy for Tidal supporting `HI_RES_LOSSLESS`, `LOSSLESS`, `HIGH`, `LOW` and Dolby Atmos. It could serve `FULL`, unlike the anonymous community hosts — but there is no proxy slot left to point it at.

**Qobuz** — native downloader removed 2026-07.
- **musicdl.me**, **dab.yeet.su**, **dabmusic.xyz**

**Amazon Music** — native downloader removed 2026-07.
- **spotbye** — `https://amazon.spotbye.qzz.io`

**Deezer** — native downloader removed 2026-07.
- **deezmate** — `https://api.deezmate.com`

Thanks to everyone who ran these while the app depended on them.

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

**`afkarxyz/ffmpeg-binaries`** — ⚠️ **gone: the repository 404s as of 2026-08-04.**
It was used on first launch by the legacy desktop build to auto-install `ffmpeg`
and `ffprobe` on Windows, Linux and macOS. The link is left unlinked rather than
removed, because the code path that reads it still exists and anyone hitting it
deserves to know why it fails.

The Docker image never used it: FFmpeg comes from
[BtbN/FFmpeg-Builds](https://github.com/BtbN/FFmpeg-Builds), fetched and
checksum-verified in the Dockerfile's build stage.

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

SpotiFLAC Web is intended for personal use with content you have the right to access. The community services listed above are operated by their respective maintainers and are not affiliated with this project.
