# The download engine image

This directory builds `ghcr.io/sos-pc/spotiflac-engine`: upstream's published
package plus our HTTP shim, plus whatever patches upstream has not adopted yet.

Upstream is **`mornye`** (PyPI, [mornyeapp/Mornye-Python](https://github.com/mornyeapp/Mornye-Python)).
Until 2026-10-20 the same project shipped as `SpotiFLAC` from
`BartolomeoRusso9/SpotiFLAC-Module-Version`. After Spotify's legal notice to the
SpotiFLAC author it was renamed, its history and tags were reset, and the
`SpotiFLAC` package stopped at 5.0.6. For this image the move was mechanical —
`AsyncSpotiFLAC` became `AsyncMornye`, `SpotiFLAC.*` became `mornye.*`, the data
directory `~/.spotiflac` became `~/.mornye` — which is the shim's contract doing
its job: the Go app did not change.

```
Dockerfile             builds the image (context = this directory)
shim.py                the engine-agnostic HTTP adapter the Go app calls
requirements.txt       the shim's own deps
patches/*.patch        applied to the installed package at build time (none today)
pin-extensions.py      holds a provider bundle at an older version, when needed (none today)
record-extensions.py   writes the installed bundles into the image (/health shows them)
contract-check.py      fails the build if the image could not download (see below)
compare-engines.py     real downloads through an engine, to compare two images
```

## No browser in the image

A provider gateway can challenge a new session with Cloudflare Turnstile. The
SpotiFLAC image answered that with a browser of its own — Xvfb, Chromium run
without its sandbox, a wrapper to pass the flags, an entrypoint to start the
display — and reached the deployment's external solver only through a hook,
after its own browser had failed.

`mornye` takes the external solver natively: `TURNSTILE_SOLVER_URL`, documented
upstream as "an external Turnstile solver service to use instead of the built-in
browser". This image therefore carries no browser at all. It is a third smaller,
and it no longer runs Chromium without a sandbox next to third-party JavaScript.

**The trade-off:** set `TURNSTILE_SOLVER_URL` on this service. Without it, or
with that service down, a session that needs a challenge cannot be obtained and
the providers that need one fail until it is back. `shm_size` is no longer
needed — it was for Chromium.

**Do not set `read_only: true` on this service.** It writes extension data and
signed sessions under `~/.mornye`.

## What the build refuses to publish

`contract-check.py` runs inside `docker build` and fails it, rather than letting
a scheduled rebuild move `:latest` to an image that cannot download:

- every keyword `shim.py` passes to `AsyncMornye(...)` still exists;
- every module `shim.py` imports still imports;
- each service the app can delegate (qobuz, deezer, amazon, tidal) resolves to
  an installed extension, and that extension **starts**;
- every call that extension makes into the host (`file.*`, `http.*`, `utils.*`
  and the rest) lands on something the bridge actually defines — the check that
  would have stopped September's `file.downloadSegments is not a function`.

A build that fails here leaves production on its last good image.

## Why there is no fork any more

There used to be one — `sos-pc/SpotiFLAC-Module-Version` — with a `git merge
upstream/main` ritual to keep it current. It existed to carry two things: this
directory, and two edits to the engine's core. Neither needed a fork:

- this directory belongs in *our* repo, where our CI and reviews reach it;
- the core installs from PyPI, and the edits are `.patch` files applied on top.

That removes a whole class of work. There is no merge to perform, no conflict to
resolve, no drift to monitor on a schedule. **If a fix we carry stops reaching
its target, the build says so immediately**, at the point of change, instead of
being discovered weeks later by a cron job nobody reads.

"Says so" rather than "fails": a patch whose bug upstream has fixed is skipped
with a notice, because failing there would freeze this image on its previous
version and cut the deployment off from every other upstream change. Failing is
reserved for the case that deserves it — a fix that is still needed and no
longer applies. See [`patches/README.md`](patches/README.md).

Full reasoning and the measurements behind it:
[docs/upstream-tracking-plan.md](../docs/upstream-tracking-plan.md).

## Keep behaviour in the shim, not in patches

Every patch is a liability: it can rot if upstream rewrites the function around
it, and it has to be re-verified on each release. So reach for one last.

1. **Use their parameters.** Tagging is off via `enrich_metadata=False` /
   `embed_lyrics=False`; providers and order come from the `services` list. Most
   of what we want is a parameter away.
2. **Then use the shim.** Anything expressible as "do something before or after
   their code" belongs in `shim.py`. `_quiet_engine_logs()` is the model: it
   works around upstream's console interception from the outside, without
   touching their source.
3. **Only then, a patch.** When the change has to happen *inside* one of their
   functions and nothing around the call reaches it.

A patch is also the right shape for the opposite reason: it fails loudly. A
monkey-patch redefining one of their functions from a copy would keep applying
cleanly while silently going stale — a worse failure than a conflict.

## Patches and pins currently carried

**None.** The last patch, `amazon-songlink-unformatted-url.patch`, was retired on
2026-08-15 when upstream rewrote that path. The last pins went with the move to
`mornye`: tidal-web on 2026-09-23 and deezer on 2026-10-21, each once the bridge
implemented what the newer bundle calls. `pin-extensions.py` keeps the mechanism
for the next incompatible bundle; `contract-check.py` is what will name it.

## Where the download providers come from

**Not from the wheel.** Since SpotiFLAC 3.0.0 the providers are JavaScript
extensions fetched from a registry, and `mornye` bundles no registry at all — by
design: the operator names one in `MORNYE_REGISTRIES` (formerly
`SPOTIFLAC_REGISTRIES`). A headless image has nowhere else to get it from.

The Dockerfile therefore installs them **at build time**, with the registry URL
as an `ARG` that deliberately does not become an `ENV`:

| | at build | at runtime |
|---|---|---|
| `MORNYE_REGISTRIES` | set, from the `ARG` | **unset** |
| effect | the registry's extensions installed into the image | manager skips its registry check and uses what is installed |

That is what makes the image reproducible — `MORNYE_VERSION` pins the wheel, and
this pins the code that actually downloads — and what stops every container
recreation from re-fetching every bundle from GitHub.

An operator who wants runtime updates can set `MORNYE_REGISTRIES` in compose.
Then it is a choice.

**The exposure, stated:** the bundles are executable JavaScript from a
third-party repository, and their `sha256` comes from the same registry that
serves them — good against corruption, not against a compromised registry.
Pinning them into an image someone can inspect is better than fetching them
fresh on every start; it is not the same as trusting them.

Full measurements: [docs/engine-3.0-impact-plan.md](../docs/engine-3.0-impact-plan.md).

## Comparing two engine images

A green build does not prove a track comes out the other end. Before moving
production from one image to another, run the same tracks through both, each
inside its own container:

```bash
docker cp engine/compare-engines.py spotiflac-engine:/tmp/ && docker exec spotiflac-engine python3 /tmp/compare-engines.py --label current --jsonl /tmp/current.jsonl
```

Then the same against the candidate container with `--label candidate`, and
`--compare current.jsonl candidate.jsonl` to read them side by side. Failures are
filed by cause — `session` (challenge or signed session), `not_found`,
`network`, `bad_file` (the file is not the audio its name claims) — so a
difference in how two images obtain sessions is not mistaken for a provider
that lacks the track. Pass your own tracks with `--tracks`.

## Adding or updating a patch

Write the probe first — [`patches/README.md`](patches/README.md) says why and
shows one. Then generate the diff against the version you are targeting, never
against a working tree:

```bash
# extract the wheel you are targeting, edit a copy, then:
diff -u a/mornye/core/<file>.py b/mornye/core/<file>.py > patches/<name>.patch
```

Paths inside the patch must start at `mornye/` — the Dockerfile applies with
`-p1` from the site-packages root.

Verify before committing — both halves:

```bash
patch -p1 --dry-run -d <extracted-wheel> < patches/<name>.patch
```

```bash
PYTHONPATH=<extracted-wheel> python patches/<name>.probe.py; echo "probe exit=$? (0=bug present, 3=gone)"
```

## Building locally

```bash
docker build -t spotiflac-engine ./engine
docker build -t spotiflac-engine --build-arg MORNYE_VERSION=1.0.3 ./engine
```

Without the arg you get whatever is current on PyPI. CI always passes it
explicitly, and tags the image `mornye-<version>` — the project restarted its
numbering at 1.0.0, and SpotiFLAC images already used 1.6.x and 1.7.x tags.
