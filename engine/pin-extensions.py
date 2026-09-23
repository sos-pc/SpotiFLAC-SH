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

Reinstalls the named extensions from a pinned commit, verified by digest.

The pins were the newest version of each whose declared features this host
satisfied, found by walking the registry repo's history. Verified by real
downloads on two tracks: both providers failed on both tracks before, both
succeed on both after.

─── How to know whether a pin is still needed ───────────────────────────────

Not from the manifests. This file used to print, for every installed
extension, the declared `requiredRuntimeFeatures` this host lacks - and that
report cried wolf: four download providers out of five declared
`preparedContext@1`, and all four read it as
`options && options.preparedContext || {}` and do without it (read
2026-09-23: amazon, deezer, qobuz-web, soundcloud). A declaration says what the author's host offers, not what the code
cannot live without.

What decides is whether the bundle CALLS a host method the bridge does not
define, without testing for it first. contract-check.py now checks exactly
that for every service the shim serves, after this file has run, and fails
the build on it - so a bundle update that would break downloads never reaches
`:latest`, and the build log names the call.

─── When a pin should go ────────────────────────────────────────────────────

The moment the module implements what the newer bundle calls. Delete the entry,
rebuild, and let contract-check.py say whether the current version passes.
A pin here is a splint, not a decision about what we want.

tidal-web went that way on 2026-09-23: SpotiFLAC 4.x added
`file.downloadSegments` (absent from 3.8.0, present in 4.3.0), and
contract-check.py passes tidal-web 1.2.6 on 4.3.0 with a single missing call,
`file.exists`, which sits inside a try/catch (see KNOWN_HARMLESS_CALLS there).
"""
from __future__ import annotations

import hashlib
import pathlib
import shutil
import urllib.request

REGISTRY_RAW = "https://raw.githubusercontent.com/zarzet/SpotiFLAC-Extension"

# id -> (commit, sha256 of the .sflx, version, why)
#
# The commit is a real ref, not a branch: the branch path serves whatever is
# current, which is the thing that broke us.
PINS: dict[str, tuple[str, str, str, str]] = {
    "deezer": (
        "923d942f3e",
        "6320680f44f8292b16e3d83bc789305434442198820a0f11b4416538b143b04b",
        "1.3.1",
        "1.3.3 added patternedFileTransform@1 + preparedContext@1",
    ),
}

# Not pinned, deliberately:
#
#   tidal-web  pinned at 1.2.0 until 2026-09-23 - see "When a pin should go"
#              above. contract-check.py is what would say if it needs one again.
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
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
