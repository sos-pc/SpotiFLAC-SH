# The download engine image

This directory builds `ghcr.io/sos-pc/spotiflac-engine`: upstream's published
package plus our HTTP shim, plus whatever patches upstream has not adopted yet.

```
Dockerfile            builds the image (context = this directory)
docker-entrypoint.sh  starts Xvfb, then hands over to uvicorn
shim.py               the engine-agnostic HTTP adapter the Go app calls
requirements.txt      the shim's own deps
patches/*.patch       applied to the installed package at build time
hooks/*.py            runtime wrappers loaded by shim.py at startup
```

## The image must carry a browser

Several provider routes drive Chromium through `pydoll`. Upstream's own
Dockerfile therefore installs `xvfb`, `chromium` and `fonts-liberation`, sets
`DISPLAY=:99`, and starts Xvfb in its entrypoint before the application.

Ours did none of that for a while: the package list here was ported from theirs
with those three dropped. Nothing caught it, because the gap only surfaces when
a route actually reaches for a browser, and it surfaces as
`[Errno 2] No such file or directory: 'Xvfb'` — which reads like an engine bug
rather than our packaging.

Two consequences worth keeping in mind:

- **`shm_size: 1g` in compose.** Chromium puts renderer shared memory in
  `/dev/shm` and Docker's default is 64 MB. The wrapper below also passes
  `--disable-dev-shm-usage`, so this is now a safety net rather than a
  requirement — keep it anyway.
- **Do not set `read_only: true` on this service.** It writes JS extensions, a
  browser profile and the X socket.

### Why `/usr/bin/chromium` is a wrapper script

Upstream gates Chromium's container flags on being root:

```python
# SpotiFLAC/core/solver.py
_docker_flags = []
if os.name != "nt" and hasattr(os, "geteuid") and os.geteuid() == 0:
    _docker_flags = ["--no-sandbox", "--disable-dev-shm-usage"]
```

Their image runs as root; ours runs as uid 1000, which is what makes `/staging`
shareable with the Go app and is not up for negotiation. So the list stays empty
and every browser launch dies with `No usable sandbox!` — `cap_drop: ALL` blocks
the user-namespace sandbox and `no-new-privileges` blocks the setuid helper.

The Dockerfile therefore moves the real binary to `/usr/bin/chromium.real` and
puts a wrapper at `/usr/bin/chromium` that appends the same two flags. It lives
in our packaging rather than in their source, so there is nothing to re-verify
on each release and no patch that can rot.

**This drops Chromium's internal sandbox**: a compromised renderer holds
container privileges. The container is already non-root, capability-dropped,
`no-new-privileges` and unpublished — and the alternative, restoring `SYS_ADMIN`
or dropping `no-new-privileges`, is strictly worse.

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

1. **Use their flags.** Tagging is off via `enrich_metadata=False` /
   `embed_lyrics=False`; providers and order come from the `services` list. Most
   of what we want is a parameter away.
2. **Then use the shim.** Anything expressible as "call something before or
   after their code" belongs in `shim.py`. `_prime_tidal_apis()` is the model:
   upstream ships a priming function and never calls it, so we call it — without
   touching their source.
3. **Only then, a patch.** When the change has to happen *inside* one of their
   functions and no call-order trick reaches it.

A patch is also the right shape for the opposite reason: it fails loudly. A
monkey-patch redefining one of their functions from a copy would keep applying
cleanly while silently going stale — a worse failure than a conflict.

## Patches currently carried

**None.** The one there was — `amazon-songlink-unformatted-url.patch` — was
retired on 2026-08-15: upstream 3.0.0 rewrote the whole resolution path into
`core/link_resolver.py`, where the SongLink call passes
`params={"url": url, "userCountry": "US"}` against a bare base URL. The
unformatted template it fixed no longer exists to fix.

That retirement is also why the build now runs a probe before each patch — see
[`patches/README.md`](patches/README.md). Upstream adopting our fix used to
fail the build and freeze this image on its previous version.

## Where the download providers come from

**Not from the wheel.** SpotiFLAC 3.0.0 deleted `SpotiFLAC/providers/` — eight
Python modules — and replaced them with JavaScript extensions fetched from a
registry. 1.7.3 carried that registry's URL as a hardcoded constant; 3.0.0
removed it and expects `SPOTIFLAC_REGISTRIES` from the environment, a `.env`
file, or the GUI settings screen, none of which a headless image has.

The Dockerfile therefore installs them **at build time**, with the registry URL
as an `ARG` that deliberately does not become an `ENV`:

| | at build | at runtime |
|---|---|---|
| `SPOTIFLAC_REGISTRIES` | set, from the `ARG` | **unset** |
| effect | seven extensions installed into the image | manager skips its registry check and uses what is installed |

That is what makes the image reproducible again — `SPOTIFLAC_VERSION` pins the
wheel, and this pins the code that actually downloads — and what stops every
container recreation from re-fetching seven bundles from GitHub.

An operator who wants runtime updates can set `SPOTIFLAC_REGISTRIES` in compose.
Then it is a choice.

**The exposure, stated:** the bundles are executable JavaScript from a
third-party repository, and their `sha256` comes from the same registry that
serves them — good against corruption, not against a compromised registry.
Pinning them into an image someone can inspect is better than fetching them
fresh on every start; it is not the same as trusting them.

Full measurements: [docs/engine-3.0-impact-plan.md](../docs/engine-3.0-impact-plan.md).

## Runtime hooks

Behaviour that must happen *inside* an upstream function, but where a textual
patch would be fragile (the function moves, the surrounding lines change), lives
in `hooks/` as Python modules loaded by `shim.py` at startup.

- **`solver_fallback.py`** — wraps `run_community_verification` in
  `signed_session_desktop.py` to try the external solver at
  `TURNSTILE_SOLVER_URL` when upstream's own modes (GUI browser, internal
  solver.py) have both failed. Survives line shifts — only breaks if the
  function is renamed, removed, or its signature changes incompatibly.

**When upstream adopts a patch textually**, `patch --dry-run` reports
"Reversed (or previously applied)". **When upstream fixes the bug their own
way**, as in 3.0.0, the patch simply stops finding its target. Both used to
fail the build, which is the wrong answer for a bug that no longer exists: it
freezes this image on its previous version and cuts the deployment off from
every other upstream change until a human intervenes.

A patch's probe is what separates those from a real breakage. See
[`patches/README.md`](patches/README.md).

**When a hook stops applying**, it logs a warning at import time and degrades
gracefully — the build succeeds, the image is published, only the fallback is
unavailable. The build log records whether each hook applied or skipped.

## Adding or updating a patch

Write the probe first — [`patches/README.md`](patches/README.md) says why and
shows one. Then generate the diff against the version you are targeting, never
against a working tree:

```bash
# extract the wheel you are targeting, edit a copy, then:
diff -u a/SpotiFLAC/core/<file>.py b/SpotiFLAC/core/<file>.py > patches/<name>.patch
```

Paths inside the patch must start at `SpotiFLAC/` — the Dockerfile applies with
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
docker build -t spotiflac-engine --build-arg SPOTIFLAC_VERSION=1.5.9 ./engine
```

Without the arg you get whatever is current on PyPI. CI always passes it
explicitly, so the image tag and the `org.opencontainers.image.version` label
match what is actually installed.
