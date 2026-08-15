# The engine's core changed shape — SpotiFLAC 1.7.3 → 3.0.0

> **🧭 Plan, 2026-08-15.** Every number here was measured against the real
> images and the real deployment; the commands are inline so they can be re-run.
> Written after 3.0.0 was built, deployed, found unable to download anything,
> and rolled back. The sidecar is **pinned to `1.7.3`** in the live compose and
> is working. Companions: [upstream-tracking-plan.md](upstream-tracking-plan.md)
> · [../engine/README.md](../engine/README.md) ·
> [../engine/patches/README.md](../engine/patches/README.md).
>
> Every section carries its status. The incident that produced this document is
> in issue #100.

## 0. The finding

This is not a version bump with a broken variable. **The download providers left
the Python package and became JavaScript bundles fetched at runtime from a
third-party GitHub registry.**

Everything below follows from that one sentence. `SpotiFLAC/providers/` — eight
Python modules, installed by pip, pinned by `SPOTIFLAC_VERSION`, present in the
image, auditable — does not exist in 3.0.0. In its place: an extension manager
that downloads `index.js` bundles from `zarzet/SpotiFLAC-Extension` on first use.

The immediate breakage (no registry configured, so nothing installs) is the
smaller half of this. The larger half is that **our reproducibility guarantee no
longer covers the code that performs downloads.**

## 1. What was measured

| Question | 1.7.3 | 3.0.0 |
|---|---|---|
| Registry URL | `REGISTRY_URL` **hardcoded** in `extensions/manager.py` | **removed** — must come from `SPOTIFLAC_REGISTRIES`, a `.env` file, or the GUI settings screen |
| `SpotiFLAC/providers/` | 8 Python modules | **absent** |
| Provider implementation | Python, in the wheel | **JavaScript**, fetched at runtime |
| Python extensions installed | — | **0** (`find_python_extension` returns `None` for all four services) |
| Extensions installed | — | **7**, all JS, **572 KB** total |
| Node in the image | v20.19.2 | v20.19.2 (npm absent — bundles are self-contained) |
| Downloads with no registry | n/a | **all fail**, every service |
| Downloads with the registry set | works | **works** — verified on two real tracks |

```bash
# where the registry comes from, per version
docker exec spotiflac-engine python -c \
  "import inspect;from SpotiFLAC.extensions import manager as M;print([n for n in dir(M) if n.isupper()])"
```

**The registry, as served on 2026-08-15** — 9 entries, every one carrying a
`sha256`:

| id | version | category | our services |
|---|---|---|---|
| `qobuz-web` | 1.1.0 | download | **qobuz** |
| `deezer` | 1.2.0 | download | **deezer** |
| `amazon` | 2.2.1 | download | **amazon** |
| `tidal-web` | 1.1.7 | download | **tidal** |
| `pandora` | 1.0.8 | download | — |
| `soundcloud` | 1.0.5 | download | — |
| `ytmusic-spotiflac` | 2.3.9 | download | — |
| `apple-music` | 1.3.8 | integration | — (not auto-installed) |
| `spotify-web` | 1.9.14 | integration | — (not auto-installed) |

The four services `ENGINE_SERVICES` delegates are all present, all JS. Three
further download providers exist that this deployment does not use.

## 2. What is broken today

### 2a. No registry configured — every download fails

**Open. This is why the sidecar is pinned.**

`ensure_download_providers()` returns at its first guard when no registry is
configured, logging **at DEBUG**:

```
[ExtMgr] No registry URLs configured; skipping automatic startup bootstrap
```

Nothing installs, so `_build_providers_for_name()` returns an empty list for
every name, and `downloader.py` raises:

```
ValueError("No valid providers found in: ['qobuz', 'deezer', 'amazon']")
```

The message names the service list, which is the one thing that is not wrong.

**Measured fix**: with `SPOTIFLAC_REGISTRIES` set to the URL 1.7.3 carried
internally, seven extensions install and downloads succeed — checked on
`qobuz,deezer,amazon` and on `tidal` alone, both producing real FLACs.

### 2b. `_prime_tidal_apis()` imports a module that is gone

**Open.** `shim.py` imports `SpotiFLAC.providers`, absent in 3.0.0. It degrades
to a logged warning, so it is not fatal — but the priming it performs does not
happen. A Tidal download succeeded without it (the `tidal-web` extension carries
its own API handling), which suggests the function is now obsolete. **One track
is not proof.** Decide, do not assume.

### 2c. `/providers/health` said `extensions: ok` with zero extensions installed

**Open.** During the incident the health endpoint reported
`extensions: {"ok": true, "detail": "ok"}` while nothing was installed and every
download was failing. A status board that answers "fine" in that state is worse
than one that says nothing.

## 3. What changed in kind

These are not bugs. They are consequences that need decisions.

### 3a. Version pinning no longer covers the downloaders

`SPOTIFLAC_VERSION=3.0.0` pins the wheel. It does not pin `qobuz-web@1.1.0` or
`amazon@2.2.1`. Two builds of the same Dockerfile, a week apart, can ship
different download code. The image tag stops meaning what it meant.

`install_from_url(url, sha256=...)` exists and takes both — so pinning is
*possible*, by installing named versions rather than asking the registry for
current. There is no `version=` parameter; the pin is the URL plus its digest.

### 3b. Supply chain: third-party executable code

The bundles are JavaScript from a GitHub repository that is neither ours nor the
engine author's namespace. Integrity is checked with a `sha256` **supplied by
the same registry that serves the file** — that detects corruption and
mid-flight tampering, not a compromised or malicious registry.

This is the same class as `spotFetchAPIUrl` (issue #73), one step closer to the
core: that one returns data, this one returns code that runs in our container.

Upstream removing the hardcoded default is plausibly deliberate. Re-adding it is
a choice, and it should be made rather than inherited.

### 3c. Node is now load-bearing for every download

It used to carry one optional route. Every provider is now a JS bundle, so
`nodejs` in the image is on the critical path for all downloads. It is present
(v20.19.2) and was already installed for other reasons — but it is no longer
optional, and nothing in the build asserts it.

### 3d. Extensions are re-fetched on every container recreation

They install to `/home/nonroot/.spotiflac/extensions`. The compose mounts
`engine_cache:/home/nonroot/.cache` — **a different directory**. Nothing
persists them.

So GitHub's availability becomes a startup dependency for downloading at all,
every time the container is recreated. 572 KB is nothing to fetch; being unable
to fetch it is everything.

### 3e. Three download providers exist that we do not offer

`pandora`, `soundcloud`, `ytmusic-spotiflac` install alongside the four we use
and are simply never named. Whether to expose them is a product decision, not a
packaging one — and `ytmusic` in particular is a different kind of source from
the lossless four.

## 4. Decisions that are the operator's

| # | Decision | Recommendation |
|---|---|---|
| **D1** | Where the registry is configured: baked into the image, or set in the compose | **Image**, as a build arg with upstream's own URL as default, so the image works standalone |
| **D2** | Install extensions **at build time** (pinned, shipped) or let them fetch at runtime | **Build time.** It restores what `SPOTIFLAC_VERSION` used to guarantee, removes the startup network dependency, and moves failure from the first download to the build |
| **D3** | Pin extension versions explicitly, or follow the registry's current | Follow current **at build time** — pinned by the image, refreshed on every rebuild, which is how the wheel is already handled |
| **D4** | Expose `pandora` / `soundcloud` / `ytmusic` | **Not now.** Out of scope of a repair; revisit deliberately |

**Decided by the operator, 2026-08-15: D1, D2 and D3 as recommended; D4 stays
open.** Implemented in #102 as an `ARG` that deliberately does not become an
`ENV`, so the variable does not survive into the running image — measured: with
it unset at runtime the manager skips its registry check, uses what the build
installed, and a real download succeeds.

D2 is the load-bearing one. Everything else follows from it.

## 5. The responses, in order

1. **Make the build prove providers resolve.** `contract-check.py` today asserts
   `AsyncSpotiFLAC`'s kwargs and attrs — all of which were **intact** while the
   engine could not download a single track. Two additions would have caught
   this before it ever reached the server:
   - import every module `shim.py` imports (catches `SpotiFLAC.providers`);
   - assert each default service name resolves to at least one provider.

   This is the durable piece. Do it first, so the rest is verified rather than
   hoped.
2. **Configure the registry and install at build time** (D1 + D2 + D3).
3. **Decide `_prime_tidal_apis`** — port it or delete it (§2b).
4. **Fix the health signal** so `extensions.ok` reflects installed providers
   (§2c).
5. **Unpin the sidecar**, redeploy, and verify with a real download — the only
   check that has caught anything so far.
6. **Then the Go side**: `ENGINE_SERVICES`, `autoOrder`, and the settings screen
   still describe a world of Python providers. Nothing there is broken today,
   but the vocabulary has drifted.

## 6. What was not measured

Stated so nobody mistakes this document for more than it is:

- **Quality tiers.** *Partly closed by #102*: a second track downloaded through
  `tidal` at `HI_RES_LOSSLESS` came back a valid 39 MB FLAC at 24-bit, against
  16-bit for the `LOSSLESS` run — so bit depth is honoured. `qobuzQuality=27`,
  `autoQuality=24` and `allowFallback=false` remain unverified.
- **Tracks.** *Partly closed by #102*: two different tracks now, across two
  quality tiers. Still nothing about playlists, albums, ISRC fallback, or a
  track that is genuinely hard to source.
- **`allow_fallback`** semantics through the extension path.
- **Rate limits and long-run behaviour** of a third-party registry consulted on
  every container start.
- **The `integration` extensions** (`apple-music`, `spotify-web`), which our
  path does not install and which may or may not matter for metadata.
