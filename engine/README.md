# The download engine image

This directory builds `ghcr.io/sos-pc/spotiflac-engine`: upstream's published
package plus our HTTP shim, plus whatever patches upstream has not adopted yet.

```
Dockerfile          builds the image (context = this directory)
shim.py             the engine-agnostic HTTP adapter the Go app calls
requirements.txt    the shim's own deps
patches/*.patch     applied to the installed package at build time
```

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

- **`solver-fallback-desktop.patch`** — `signed_session_desktop.py`: when stdin
  is not a TTY (a container), try the external solver at `TURNSTILE_SOLVER_URL`
  before giving up. Interactive behaviour is untouched when a TTY exists.

  The same change for `signed_session_mobile.py` was carried here too, until
  upstream adopted it independently in `dc35550b` (2026-07-28) — same function,
  same call site, reformatted. It was dropped rather than duplicated. The
  desktop one may well go the same way; no PR was opened for either.

**When upstream adopts a patch**, `patch --dry-run` reports "Reversed (or
previously applied)" and the build fails. That is the signal to delete the file.

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
