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

It was too narrow a second time, in September. Every service resolved, every
bundle started in well under a second, and tidal-web 1.2.2 still failed every
download with `file.downloadSegments is not a function`: the bundle called a
host method this module had never implemented. Starting a bundle proves it
loads. It says nothing about whether the host provides what it calls, and the
bundles are written for SpotiFLAC Mobile's host, which is ahead of this one.
So every call a bundle makes into the host is now compared with what this
module's JavaScript bridge actually defines.

Run it locally the same way CI does:

    python contract-check.py
"""

from __future__ import annotations

import ast
import inspect
import pathlib
import re
import sys
import time
from collections import Counter

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

# The objects SpotiFLAC Mobile's runtime puts in every extension's global scope
# (go_backend, `vm.Set("file", ...)` and its siblings). The bundles are written
# against that runtime, so these are the names worth looking for in them. Which
# methods each object has on THIS host is not listed here: it is read out of
# the bridge the bundles actually run under, at build time.
HOST_OBJECTS = (
    "file", "http", "log", "session", "utils", "gobackend", "matching",
    "storage", "credentials", "ffmpeg", "auth", "convert",
)

# Calls that reach for a method this host lacks, but that someone has read and
# shown to be harmless. Keyed by extension id and call; the number is how many
# call sites were read. A bundle that grows another one fails again, so a new
# call has to earn the same verdict instead of inheriting it.
KNOWN_HARMLESS_CALLS: dict[tuple[str, str], tuple[int, str]] = {
    ("tidal-web", "file.exists"): (
        1,
        "only inside deleteQuietly(), which wraps it in try/catch: the call throws "
        "into that catch and a temporary file is left behind, the transfer itself "
        "is unaffected (read in tidal-web 1.2.0 and 1.2.6, 2026-09-23)",
    ),
}


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


# ── Reading JavaScript without a JavaScript parser ────────────────────────────
#
# Crude on purpose, and checked against the real files rather than trusted: on
# 2026-09-23 these read the SpotiFLAC 3.8.0 and 4.3.0 bridges and six bundles,
# flagged the file.downloadSegments call that broke tidal-web on 3.8.0, and
# cleared the same bundle on 4.3.0. Where they could be fooled they fail CLOSED
# - a bridge they cannot read, or a provider bundle in which they find no file
# call at all, is reported as a problem, never as a pass.

def _strip_js_comments(js: str) -> str:
    """Comments out, so a method named in prose is not mistaken for a call.

    A `//` preceded by `:` is kept: that is every URL in these files.
    """
    js = re.sub(r"/\*.*?\*/", " ", js, flags=re.S)
    return re.sub(r"(?<!:)//[^\n]*", " ", js)


def _blank_nested(body: str) -> str:
    """The body with strings and everything below depth 0 blanked out."""
    out: list[str] = []
    depth, quote, i = 0, None, 0
    while i < len(body):
        c = body[i]
        if quote:
            if c == "\\":
                out.append("  ")
                i += 2
                continue
            if c == quote:
                quote = None
            out.append(" ")
        elif c in "'\"`":
            quote = c
            out.append(" ")
        else:
            if c in "{([":
                depth += 1
            elif c in "})]":
                depth -= 1
            out.append(c if depth == 0 else " ")
        i += 1
    return "".join(out)


def _object_literal(js: str, start: int) -> str:
    """The inside of the `{ ... }` whose opening brace ends just before `start`."""
    depth, quote, i = 1, None, start
    while i < len(js) and depth:
        c = js[i]
        if quote:
            if c == "\\":
                i += 2
                continue
            if c == quote:
                quote = None
        elif c in "'\"`":
            quote = c
        elif c == "{":
            depth += 1
        elif c == "}":
            depth -= 1
        i += 1
    return js[start:i - 1]


def _bridge_api(bridge_js: str) -> dict[str, set[str]]:
    """What each host object provides on this host, read from its _bridge.js.

    The bridge installs them as `global.<name> = { ... }` and aliases one as
    another (`global.gobackend = global.utils`); a property is `name:` or a
    method shorthand `name(`.
    """
    js = _strip_js_comments(bridge_js)
    api: dict[str, set[str]] = {}
    for m in re.finditer(r"\bglobal\.([A-Za-z_]\w*)\s*=\s*\{", js):
        keys = re.findall(
            r"(?:^|[,{\n])\s*(?:async\s+)?([A-Za-z_$][\w$]*)\s*(?::|\()",
            _blank_nested(_object_literal(js, m.end())),
        )
        api.setdefault(m.group(1), set()).update(keys)
    for m in re.finditer(r"\bglobal\.([A-Za-z_]\w*)\s*=\s*global\.([A-Za-z_]\w*)\s*;", js):
        api[m.group(1)] = set(api.get(m.group(2), set()))
    return api


def _host_calls(bundle_js: str) -> Counter:
    """(object, method) -> number of call sites, for every name in HOST_OBJECTS."""
    js = _strip_js_comments(bundle_js)
    pattern = r"(?<![\w$.])(%s)\s*\.\s*([A-Za-z_$][\w$]*)\s*\(" % "|".join(HOST_OBJECTS)
    return Counter(re.findall(pattern, js))


def _is_guarded(bundle_js: str, obj: str, method: str, api: dict[str, set[str]]) -> bool:
    """Whether the bundle tests for this call before making it.

    `typeof obj.method` protects a missing method. A bare `typeof obj` only
    protects a missing OBJECT: where the host has `obj` without the method,
    the guarded branch runs and the call throws all the same.
    """
    if re.search(r"typeof\s*\(?\s*%s\s*\.\s*%s\b" % (obj, method), bundle_js):
        return True
    return obj not in api and bool(re.search(r"typeof\s*\(?\s*%s\b(?!\s*\.)" % obj, bundle_js))


def _check_host_calls(service: str, ext: str, installed, api: dict[str, set[str]],
                      problems: list[str]) -> None:
    """Every call the bundle makes into the host must land on something.

    An unguarded call to a method the bridge does not define is a download that
    fails with `... is not a function` the moment it reaches that line - which
    is the September failure, and which nothing before this could see.
    """
    root = pathlib.Path(installed.index_js).parent
    try:
        source = "".join(p.read_text(encoding="utf-8", errors="replace")
                         for p in sorted(root.rglob("*.js")))
    except OSError as exc:
        problems.append(f"cannot read the {ext!r} bundle to check its host calls: {exc}")
        return

    calls = _host_calls(source)
    if not any(obj == "file" for obj, _ in calls):
        # A download provider writes its file through the host; finding no such
        # call means the reader is out of step with the bundle, not that the
        # bundle is fine.
        problems.append(
            f"found no file.* call in the {ext!r} bundle, so its host calls could "
            "not be checked - the reader in contract-check.py needs updating"
        )
        return

    guarded: list[str] = []
    harmless: list[str] = []
    for (obj, method), sites in sorted(calls.items()):
        if method in api.get(obj, set()):
            continue
        call = f"{obj}.{method}"
        if _is_guarded(source, obj, method, api):
            guarded.append(call)
            continue
        known = KNOWN_HARMLESS_CALLS.get((ext, call))
        if known and sites <= known[0]:
            harmless.append(f"{call} (x{sites})")
            continue
        problems.append(
            f"service {service!r} resolves to extension {ext!r}, which calls "
            f"{call} ({sites} call site{'s' if sites > 1 else ''}, no typeof guard) - "
            "this module's bridge does not define it, so every download reaching "
            "that call fails with 'is not a function'"
        )

    line = f"host calls: {service} -> {ext}: {len(calls)} methods"
    if guarded:
        line += f"; absent but guarded: {', '.join(guarded)}"
    if harmless:
        line += f"; absent, known harmless: {', '.join(harmless)}"
    print(line)


def _load_bridge_api() -> tuple[dict[str, set[str]] | None, str | None]:
    """The bridge node runs the bundles under, parsed. (api, None) or (None, why)."""
    try:
        from SpotiFLAC.extensions import runtime as js_runtime
    except Exception as exc:  # noqa: BLE001
        return None, f"cannot import the JS runtime to read its bridge: {exc}"
    # The same constant the runtime hands to node, so this reads the file that
    # actually runs rather than a guess at where it lives.
    path = getattr(js_runtime, "_BRIDGE_JS", None) or (
        pathlib.Path(js_runtime.__file__).parent / "_bridge.js")
    try:
        api = _bridge_api(pathlib.Path(path).read_text(encoding="utf-8"))
    except OSError as exc:
        return None, f"cannot read the JS bridge at {path}: {exc}"
    if "download" not in api.get("file", set()):
        return None, (
            f"could not find file.download in {path} - the bridge changed shape and "
            "the reader in contract-check.py needs updating; host calls are unverified"
        )
    return api, None


def main() -> int:
    problems: list[str] = []
    notes: list[str] = []
    # Kept apart from `problems` only because the fix is somewhere else: a
    # missing host method is solved in pin-extensions.py, not in shim.py.
    host_problems: list[str] = []

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
        bridge_api, bridge_error = _load_bridge_api()
        if bridge_error:
            host_problems.append(bridge_error)
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
                continue

            # ── and it has to START ──────────────────────────────────────────
            #
            # Installed is not the same as runnable, and everything above this
            # line only proves installed - which a successful COPY also proves.
            # Since 3.0.0 every download provider is a JavaScript bundle, so
            # `node` sits on the critical path of every download and nothing
            # asserted it could execute one. That is the exact shape of the July
            # regression, where ffmpeg was present, executable, and unable to
            # start because the runtime image had no ELF loader: green build,
            # dead feature, found by reading production logs days later.
            #
            # JSRuntime.start() spawns node and waits for the bundle to announce
            # itself - the same path a real download takes, since
            # JSExtensionProvider builds the runtime exactly this way. Offline,
            # and measured at ~200 ms per extension on the reference image.
            #
            # ext_path is the ENTRY FILE, not the directory. Handing it the
            # directory does not raise: it waits out the full startup timeout
            # and reports "Extension did not respond", which reads as a broken
            # bundle and is not one. Measured, on seven bundles that all work.
            if getattr(installed, "runtime", "javascript") != "javascript":
                continue
            try:
                from SpotiFLAC.extensions.runtime import JSRuntime
            except Exception as exc:  # noqa: BLE001
                problems.append(f"cannot import the JS runtime bridge: {exc}")
                break
            try:
                started = time.perf_counter()
                runtime = JSRuntime(ext_path=installed.index_js, settings={},
                                    startup_timeout=30.0)
                runtime.start()
                runtime.stop()
                print(
                    f"js runtime: {service} -> {ext} ready in "
                    f"{(time.perf_counter() - started) * 1000:.0f} ms"
                )
            except Exception as exc:  # noqa: BLE001
                problems.append(
                    f"service {service!r} resolves to extension {ext!r}, which is "
                    f"installed but does not start: {exc}"
                )

            # ── and everything it calls has to EXIST ─────────────────────────
            #
            # Runs even when the start above failed: the two findings do not
            # depend on each other, and one report with both is worth more than
            # two builds that each reveal half.
            if bridge_api is not None:
                _check_host_calls(service, ext, installed, bridge_api, host_problems)

    for note in notes:
        print(f"CONTRACT WARNING: {note}", file=sys.stderr)

    if problems or host_problems:
        # ASCII only, here and above: this runs inside `docker build`, whose
        # stdout encoding is not ours to choose. A decorative dash that raises
        # UnicodeEncodeError would fail the build for a reason that has nothing
        # to do with the contract, and bury the reason that does.
        print("CONTRACT BROKEN - this image would fail downloads:", file=sys.stderr)
        for p in problems + host_problems:
            print(f"  - {p}", file=sys.stderr)
        if problems:
            print(
                "\nFor the API shim.py calls: fix engine/shim.py:_run_download and "
                "this file together, then rebuild.",
                file=sys.stderr,
            )
        if host_problems:
            print(
                "\nFor a bundle calling what the bridge lacks: hold that extension "
                "at its last compatible version in engine/pin-extensions.py, or "
                "wait for upstream to implement the method. If the call has been "
                "read and shown harmless, record it in KNOWN_HARMLESS_CALLS with "
                "the number of call sites read.",
                file=sys.stderr,
            )
        print(
            "\nDo not publish this image: it would fail on the first download "
            "instead of here.",
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
            f"{len(REQUIRED_SERVICES)} services started, their host calls all defined)"
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
