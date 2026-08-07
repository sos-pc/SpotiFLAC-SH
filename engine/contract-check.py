"""Assert that upstream still offers the API shim.py calls.

Why this exists
---------------
The engine image rebuilds itself whenever upstream publishes to PyPI, and a
scheduled build tags `:latest` — so a new upstream release reaches production
without a human in the loop. That is the point, and it is also the risk: the
only thing standing between an upstream rename and a broken deployment used to
be a comment in shim.py reading "Signature verified 2026-07-23". A date is not
a check. It says someone looked once, at a version that is no longer the one
being built.

The import check in the Dockerfile catches a package that cannot load at all.
It does not catch the likelier failure: the package loads, the class is there,
and one keyword argument has been renamed. That builds green, publishes to
`:latest`, and fails on the first download.

So this asserts the exact surface shim.py depends on, and it runs at build time
— after the patches, before the image is published. An upstream release that
breaks the contract now fails the build loudly instead of reaching the server
quietly.

Run it locally the same way CI does:

    python contract-check.py
"""

from __future__ import annotations

import inspect
import sys

# Every keyword shim.py passes to AsyncSpotiFLAC(...). Keep this list and the
# call in _run_download in step: this file is the executable copy of that call's
# assumptions, and a keyword added there without being added here is unchecked.
REQUIRED_INIT_KWARGS = (
    "output_dir",
    "services",
    "quality",
    "allow_fallback",
    "enrich_metadata",
    "embed_lyrics",
    "log_level",
)

# Everything else shim.py relies on: the async-context-manager protocol, and the
# one method it calls on the client.
REQUIRED_ATTRS = ("__aenter__", "__aexit__", "download_track")


def main() -> int:
    problems: list[str] = []
    notes: list[str] = []

    try:
        from SpotiFLAC import AsyncSpotiFLAC
    except Exception as exc:  # noqa: BLE001 — any import failure is a hard stop
        print(f"CONTRACT: cannot import AsyncSpotiFLAC: {exc}", file=sys.stderr)
        return 1

    try:
        params = inspect.signature(AsyncSpotiFLAC.__init__).parameters
    except (TypeError, ValueError) as exc:
        print(f"CONTRACT: cannot inspect AsyncSpotiFLAC.__init__: {exc}", file=sys.stderr)
        return 1

    # A **kwargs in __init__ swallows any keyword, so absence proves nothing and
    # presence proves nothing either — the argument would be accepted and then
    # ignored, which is the failure mode this file exists to catch. Say so
    # rather than reporting a pass we cannot justify.
    accepts_var_kw = any(
        p.kind is inspect.Parameter.VAR_KEYWORD for p in params.values()
    )

    missing = [k for k in REQUIRED_INIT_KWARGS if k not in params]
    if missing and not accepts_var_kw:
        problems.append(
            "AsyncSpotiFLAC.__init__ no longer accepts: " + ", ".join(missing)
        )
    elif missing:
        notes.append(
            "AsyncSpotiFLAC.__init__ takes **kwargs, so these could not be "
            "verified and may now be silently ignored: " + ", ".join(missing)
        )

    for attr in REQUIRED_ATTRS:
        if not callable(getattr(AsyncSpotiFLAC, attr, None)):
            problems.append(f"AsyncSpotiFLAC.{attr} is missing or not callable")

    for note in notes:
        print(f"CONTRACT WARNING: {note}", file=sys.stderr)

    if problems:
        # ASCII only, here and above: this runs inside `docker build`, whose
        # stdout encoding is not ours to choose. A decorative dash that raises
        # UnicodeEncodeError would fail the build for a reason that has nothing
        # to do with the contract, and bury the reason that does.
        print("CONTRACT BROKEN - upstream changed the API shim.py calls:", file=sys.stderr)
        for p in problems:
            print(f"  - {p}", file=sys.stderr)
        print(
            "\nFix engine/shim.py:_run_download and this file together, then "
            "rebuild. Do not publish this image: it would fail on the first "
            "download instead of here.",
            file=sys.stderr,
        )
        return 1

    # Do not print "OK" after saying a keyword could not be verified: the whole
    # point of this file is to stop claiming guarantees it does not provide.
    if notes:
        print(
            f"contract check PASSED WITH WARNINGS "
            f"({len(REQUIRED_ATTRS)} attrs verified, kwargs unverifiable)"
        )
    else:
        print(
            f"contract check OK "
            f"({len(REQUIRED_INIT_KWARGS)} kwargs, {len(REQUIRED_ATTRS)} attrs)"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
