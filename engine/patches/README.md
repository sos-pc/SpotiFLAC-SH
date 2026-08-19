# Patches

Unified diffs applied to the **installed** upstream package at build time, from
the site-packages root with `-p1`. Paths inside a patch therefore start at
`SpotiFLAC/`.

There are none right now. This file is not a placeholder for that reason — it is
the convention every future patch has to follow, and the incident that produced
it.

## Why a patch should carry a probe

A patch that fails to apply means one of two opposite things, and `patch` cannot
tell them apart because it only reads text:

| what happened upstream | what it means for us | what the build must do |
|---|---|---|
| they **fixed the bug**, their own way | our patch is obsolete | skip it, say so, **keep building** |
| they **moved the code** around our fix | our fix is silently lost | **fail hard** |

Treating both as a failure froze this image on 2026-08-15. Upstream 3.0.0
rewrote Amazon's SongLink resolution into `core/link_resolver.py` and fixed, in
passing, the exact bug `amazon-songlink-unformatted-url.patch` existed to fix.
Four consecutive builds failed. The engine stopped following upstream — because
upstream had done something *good*.

So a patch may sit next to a probe:

```
patches/my-fix.patch
patches/my-fix.probe.py
```

The probe inspects the installed package and exits:

| exit | meaning | the build |
|---|---|---|
| `0` | the bug is still present | applies the patch |
| `3` | the bug is gone | skips it and tells you to delete the file |
| anything else | the probe could not tell | **fails** |

A patch with no probe is treated as "assume the bug is still present" — the
behaviour that predates this file. Safe, not good: it is exactly the case that
fails the build when upstream helps us.

## The one rule that matters

**`3` requires a positive determination. Everything ambiguous is a non-zero
exit.**

Three ways a probe lies, all three found by breaking one:

- **A missing dependency is not a missing module.** `from SpotiFLAC.providers
  import amazon` raises `ModuleNotFoundError` when `pydoll` is absent, exactly
  as it does when `amazon.py` is absent. A probe that catches `ImportError` and
  exits 3 reports "upstream fixed it" for a broken image, and the next build
  drops a fix nobody notices is gone. Compare `exc.name`.
- **A missing root package is not an upstream fix.** If `SpotiFLAC` itself does
  not import, the probe has learned nothing about the bug. Say so.
- **Do not grep for the line the diff replaces.** That reports "bug gone" the
  moment upstream reformats the file. Ask what the bug *is* — read the value,
  call the function, inspect the signature.

## Example

The probe the retired Amazon patch should have carried:

```python
"""Is the SongLink URL still an unsubstituted template?

0 = bug still present, 3 = gone, anything else = could not tell.
"""
import importlib
import sys

TARGET = "SpotiFLAC.providers.amazon"

try:
    amazon = importlib.import_module(TARGET)
except ModuleNotFoundError as exc:
    # If the package itself is missing, this raises and the build fails — which
    # is right, because a broken image is not an answer about the bug.
    importlib.import_module("SpotiFLAC")
    if exc.name and (exc.name == TARGET or TARGET.startswith(exc.name + ".")):
        sys.exit(3)  # the module we patch is gone; the world moved on
    raise            # one of ITS dependencies is missing; we cannot tell

sys.exit(0 if "{track_id}" in getattr(amazon, "source_url", "") else 3)
```

Against 3.0.0 that exits 3 — `SpotiFLAC.providers` no longer exists — and the
build would have continued with a line naming the file to delete, instead of
failing four times.

## Verifying before you commit

```bash
patch -p1 --dry-run -d <extracted-wheel> < patches/<name>.patch
```

The probe needs upstream's own dependencies importable, so the honest place to
run it is the image being built; the build prints its verdict on every run. A
bare `PYTHONPATH=<extracted-wheel>` check exercises the decision logic but will
report "could not tell" on any module that reaches for `pydoll` or `nodriver`.

## Prefer a hook

Before writing a patch at all, read "Keep behaviour in the shim, not in patches"
in [`../README.md`](../README.md). A runtime hook in [`../hooks/`](../hooks)
survives line shifts and degrades to a logged warning instead of stopping the
build. A patch is the right shape only when the change must happen *inside* an
upstream function and no call-order trick reaches it.
