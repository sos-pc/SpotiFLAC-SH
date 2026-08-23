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

It was too narrow once, and the way it was too narrow is worth keeping in mind
when adding to it. On 2026-08-15 every assertion below passed against SpotiFLAC
3.0.0 — the kwargs, the attrs, the health surface, all intact — while the
engine could not download a single track. Two things it did not look at:

  * the modules shim.py IMPORTS. `_prime_tidal_apis()` reached for
    `SpotiFLAC.providers`, which 3.0.0 deleted outright.
  * whether a service name still resolves to something that can download.
    3.0.0 moved every provider out of the package and into JavaScript
    extensions fetched from a registry; with no registry configured, nothing
    installed, and every name resolved to nothing.

Both are checked now. The rule they suggest: assert what the shim USES, not
only what upstream OFFERS.

Run it locally the same way CI does:

    python contract-check.py
"""

from __future__ import annotations

import ast
import inspect
import pathlib
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

# Upstream's provider reachability checker was verified here until SpotiFLAC
# 3.0.7 removed it. run_health_check_with_extensions is gone, and the
# run_health_check that remains probes lyrics servers instead, so there is no
# longer an upstream API behind GET /providers/health to hold to a contract.
# What that endpoint reports now — which services have an installed extension —
# is checked below, under "A service name must resolve to something installed",
# which was always the stronger of the two.
# Where shim.py is in the image. Read, not imported: importing it would start
# FastAPI and the hooks, and this check has no business doing that.
SHIM_PATH = "/app/shim.py"

# Every service name this image can be asked for. The first three are
# DownloadRequest's default in shim.py; "tidal" is added by the Go side when a
# token exists (ENGINE_SERVICES on the reference deployment lists all four).
#
# In 3.0.0 a name resolves through the extension catalogue to an installed
# extension. A name that resolves to nothing produces an error naming the
# service list, which reads like a caller mistake and is not one.
REQUIRED_SERVICES = ("qobuz", "deezer", "amazon", "tidal")


def _shim_upstream_imports(path: str, problems: list[str]) -> list[str]:
    """Every SpotiFLAC module shim.py imports, including inside functions."""
    try:
        source = pathlib.Path(path).read_text(encoding="utf-8")
    except OSError as exc:
        problems.append(f"cannot read {path} to check its imports: {exc}")
        return []
    try:
        tree = ast.parse(source)
    except SyntaxError as exc:
        problems.append(f"cannot parse {path}: {exc}")
        return []

    modules: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                if alias.name.split(".")[0] == "SpotiFLAC":
                    modules.add(alias.name)
        elif isinstance(node, ast.ImportFrom):
            # level > 0 is a relative import, which cannot be upstream's.
            if node.level == 0 and node.module and node.module.split(".")[0] == "SpotiFLAC":
                modules.add(node.module)
    return sorted(modules)


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

    # ── Everything shim.py imports from upstream ─────────────────────────────
    #
    # Parsed rather than listed, so it cannot drift from the file it protects.
    # ast.walk reaches imports inside function bodies too, which is where the
    # one that broke lived.
    for module in _shim_upstream_imports(SHIM_PATH, problems):
        try:
            __import__(module)
        except Exception as exc:  # noqa: BLE001 — any failure is the finding
            problems.append(f"shim.py imports {module}, which does not import: {exc}")

    # ── A service name must resolve to something installed ───────────────────
    try:
        from SpotiFLAC.extensions.catalog import extension_id
        from SpotiFLAC.extensions.manager import ExtensionManager
    except Exception as exc:  # noqa: BLE001
        notes.append(
            "the extension catalogue could not be imported, so service names "
            f"could not be verified: {exc}"
        )
    else:
        # auto_install_downloads=False on purpose: this asserts what the image
        # ALREADY carries. Letting it install here would make the check pass by
        # doing the thing it is supposed to verify has been done.
        manager = ExtensionManager(auto_install_downloads=False)
        for service in REQUIRED_SERVICES:
            try:
                ext = extension_id(service, manager)
                installed = manager.get_installed(ext) if ext else None
            except Exception as exc:  # noqa: BLE001
                problems.append(f"resolving service {service!r} raised: {exc}")
                continue
            if not installed:
                problems.append(
                    f"service {service!r} resolves to extension {ext!r}, which is "
                    "not installed in this image - every download using it would fail"
                )

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
            f"({len(REQUIRED_INIT_KWARGS)} kwargs, {len(REQUIRED_ATTRS)} attrs, "
            f"{len(REQUIRED_SERVICES)} services)"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
