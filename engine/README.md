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
resolve, no drift to monitor on a schedule. **If a patch stops applying, the
build fails** — immediately, at the point of change, instead of being discovered
weeks later by a cron job nobody reads.

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

- **`amazon-songlink-unformatted-url.patch`** — `amazon.py`: `source_url` is a
  module-level template, `"https://open.spotify.com/track/{track_id}"`, and
  nothing calls `.format()` on it. Amazon resolution step 4 therefore put the
  literal `{track_id}` on the wire and took an HTTP 400 every time; the route had
  never resolved a single track. Found in production logs 2026-08-04.

  Not a missing `f` prefix — the constant is defined where `track_id` is not in
  scope, so an f-string there would raise `NameError` at import. Only the
  substitution is missing. The ISRC variant just below it is correctly formatted
  and still 400s, so this removes a route that could not work rather than
  guaranteeing one that does.

## Runtime hooks

Behaviour that must happen *inside* an upstream function, but where a textual
patch would be fragile (the function moves, the surrounding lines change), lives
in `hooks/` as Python modules loaded by `shim.py` at startup.

- **`solver_fallback.py`** — wraps `run_community_verification` in
  `signed_session_desktop.py` to try the external solver at
  `TURNSTILE_SOLVER_URL` when upstream's own modes (GUI browser, internal
  solver.py) have both failed. Survives line shifts — only breaks if the
  function is renamed, removed, or its signature changes incompatibly.

**When upstream adopts a patch**, `patch --dry-run` reports "Reversed (or
previously applied)" and the build fails. That is the signal to delete the file.

**When a hook stops applying**, it logs a warning at import time and degrades
gracefully — the build succeeds, the image is published, only the fallback is
unavailable. The build log records whether each hook applied or skipped.

## Adding or updating a patch

Generate it against the version you are targeting, never against a working tree:

```bash
# extract the wheel you are targeting, edit a copy, then:
diff -u a/SpotiFLAC/core/<file>.py b/SpotiFLAC/core/<file>.py > patches/<name>.patch
```

Paths inside the patch must start at `SpotiFLAC/` — the Dockerfile applies with
`-p1` from the site-packages root.

Verify before committing:

```bash
patch -p1 --dry-run -d <extracted-wheel> < patches/<name>.patch
```

## Building locally

```bash
docker build -t spotiflac-engine ./engine
docker build -t spotiflac-engine --build-arg SPOTIFLAC_VERSION=1.5.9 ./engine
```

Without the arg you get whatever is current on PyPI. CI always passes it
explicitly, so the image tag and the `org.opencontainers.image.version` label
match what is actually installed.
