#!/usr/bin/env python3
"""Hold specific provider bundles at the last version this host can actually run.

─── The problem this exists for ─────────────────────────────────────────────

The extension registry serves bundles built for SpotiFLAC **Mobile**, and the
mobile app's host runtime has moved ahead of the Python module's. Each manifest
declares what it needs in `requiredRuntimeFeatures`; the module implements
`signedSession@1` and `sessionGrant@1` and nothing else. Measured on 2026-09-06
against SpotiFLAC 3.8.0 — `preparedContext`, `patternedFileTransform` and
`downloadSegments` appear in ZERO files of the installed package.

Nothing checks that before installing, so the failure lands at download time as
a JavaScript TypeError:

    tidal-web 1.2.2:  file.downloadSegments is not a function
    deezer    1.3.4:  (never reaches the transfer)

Both fail in about a second, on every track. The build stays green throughout —
the bundle loads and announces itself, which is all JSRuntime.start() proves.

─── What it does ────────────────────────────────────────────────────────────

Reinstalls the named extensions from a pinned commit, verified by digest, and
then REPORTS the feature compatibility of everything installed.

The pins are the newest version of each whose declared features this host
satisfies, found by walking the registry repo's history. Verified by real
downloads on two tracks: both providers failed on both tracks before, both
succeed on both after.

Reporting rather than gating for the rest, and that is a measured choice:
qobuz-web 1.2.10 declares `signedSession@3` and `preparedContext@1` — features
this host does not have — and downloads perfectly well. The declaration is a
manifest-level statement; whether a missing feature is ever reached depends on
the path taken. A gate would have refused a working provider.

─── When a pin should go ────────────────────────────────────────────────────

The moment the module implements the feature. Delete the entry, rebuild, and
let the registry serve current again — the report below is what tells you the
gap closed. A pin here is a splint, not a decision about what we want.
"""
from __future__ import annotations

import hashlib
import json
import pathlib
import shutil
import sys
import urllib.request

REGISTRY_RAW = "https://raw.githubusercontent.com/zarzet/SpotiFLAC-Extension"

# id -> (commit, sha256 of the .sflx, version, why)
#
# The commit is a real ref, not a branch: the branch path serves whatever is
# current, which is the thing that broke us.
PINS: dict[str, tuple[str, str, str, str]] = {
    "tidal-web": (
        "1838383e62",
        "d346f3e5fdb6f349d8f6ede1310d1961862936f64b3dabbbb4fba868cea31a9a",
        "1.2.0",
        "1.2.1 added signedSession@3 + downloadSegments@1 (registry commit 29422878, "
        '"fix(lossless): require signed session v3 host")',
    ),
    "deezer": (
        "923d942f3e",
        "6320680f44f8292b16e3d83bc789305434442198820a0f11b4416538b143b04b",
        "1.3.1",
        "1.3.3 added patternedFileTransform@1 + preparedContext@1",
    ),
}

# What the module implements, read out of its own source rather than assumed.
# Any feature name that appears nowhere in the package cannot be provided by it.
HOST_FEATURES = {"signedSession@1", "sessionGrant@1"}

# Not pinned, deliberately:
#
#   qobuz-web  declares features this host lacks and downloads anyway. Leave it
#              on current; pinning a working provider buys nothing and costs the
#              fixes its author keeps shipping.
#   amzn       is broken at 2.3.3 AND at 2.3.1 — placeholder metadata ("Amazon
#              Track B01N7QY2MB", an ASIN whose title never resolved) and a node
#              process that dies. Downgrading two versions changed nothing, and
#              the host has no memory limit and 31 GB free, so it is not ours.
#              A pin would only hide that it is theirs.


def install(manager, ext_id: str, commit: str, digest: str) -> str:
    url = f"{REGISTRY_RAW}/{commit}/extensions/{ext_id}.sflx"
    raw = urllib.request.urlopen(url, timeout=60).read()
    got = hashlib.sha256(raw).hexdigest()
    if got != digest:
        raise SystemExit(
            f"{ext_id}: pinned digest does not match what the URL served\n"
            f"  expected {digest}\n  got      {got}\n"
            "  The commit is immutable, so this means the pin is wrong or the fetch "
            "was corrupted. Not installing."
        )

    # Remove before installing, rather than letting the installer rename the old
    # directory aside. os.replace() on a directory baked into the image raises
    # EXDEV under overlayfs — the extensions live in a lower layer, and renaming
    # across layers is not supported. Measured, in this exact image.
    base = pathlib.Path.home() / ".spotiflac" / "extensions"
    for existing in base.iterdir() if base.is_dir() else []:
        if existing.is_dir() and existing.name in (ext_id, ext_id.replace("amzn", "amazon")):
            shutil.rmtree(existing)

    ext = manager.install_from_url(url, sha256=digest)
    return str(ext.manifest.get("version"))


def main() -> int:
    from SpotiFLAC.extensions.manager import ExtensionManager

    manager = ExtensionManager(auto_install_downloads=False)

    for ext_id, (commit, digest, want, why) in PINS.items():
        got = install(manager, ext_id, commit, digest)
        if got != want:
            raise SystemExit(f"{ext_id}: pinned {commit} to get {want}, installed {got}")
        print(f"pinned {ext_id} -> v{got}  ({why})")

    # ── The report: who needs what this host does not have ───────────────────
    print()
    print(f"{'extension':<22}{'version':<10}{'runtime features it needs but this host lacks'}")
    unmet = 0
    for ext in sorted(manager.list_installed(), key=lambda e: e.name):
        needs = set(ext.manifest.get("requiredRuntimeFeatures") or [])
        missing = sorted(needs - HOST_FEATURES)
        if missing:
            unmet += 1
        print(f"{ext.name:<22}{str(ext.manifest.get('version')):<10}{', '.join(missing) or '-'}")
    if unmet:
        print()
        print(
            f"{unmet} extension(s) declare a feature this host does not implement. That is "
            "not automatically a failure - qobuz-web does it and works - but it is where to "
            "look first when a provider starts returning nothing."
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
