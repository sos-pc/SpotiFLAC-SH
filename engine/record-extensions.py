#!/usr/bin/env python3
"""Write down which provider bundles this image installed, at build time.

The image had no memory of this. Extensions are resolved from a registry branch
URL during the build, unpacked into ~/.spotiflac/extensions, and that was the
end of the record: nothing in the image said which versions it carried or where
they came from. Answering "what download code is actually running?" meant
opening a shell in the container and introspecting the manager — which is not an
answer an operator can get from a status page, and not one anybody gets after
the fact from an image that has already been replaced.

Two digests per extension, because they answer different questions:

  archive_sha256  what the registry SAID it served — its own claim about the
                  .sflx package, verified by the installer against the file it
                  downloaded. Good against corruption in transit, worthless
                  against a compromised registry, since both come from there.
  entry_sha256    what this image ACTUALLY has on disk, computed here over the
                  index.js that node will execute. Ours, not theirs. Two images
                  built a week apart can be compared on this alone, whatever the
                  registry says today.

Deliberately not a lockfile. Nothing here refuses to build when the registry
moves: download providers chase hostile, changing APIs — the last registry
change was a Tidal quality fix — and a frozen provider set is a deployment that
stops working without saying why. This makes the drift visible; it does not
prevent it. Turning this into a gate is a decision, and it should be made with
these numbers in hand rather than in advance.
"""
from __future__ import annotations

import hashlib
import json
import os
import pathlib
import sys
import urllib.request

# Beside shim.py, which serves it on /health.
OUT = pathlib.Path(__file__).resolve().parent / "extensions.json"


def registry_entries() -> dict[str, dict]:
    """id -> registry entry, from every configured registry.

    Never fatal: the entries only add provenance. If the registry is unreachable
    at this point the local digests still get recorded, which is the half that
    does not depend on anyone else.
    """
    raw = os.environ.get("SPOTIFLAC_REGISTRIES") or ""
    entries: dict[str, dict] = {}
    for url in (u.strip() for u in raw.split(",") if u.strip()):
        try:
            with urllib.request.urlopen(url, timeout=30) as fh:
                doc = json.load(fh)
        except Exception as exc:  # noqa: BLE001
            print(f"registry unreadable ({url}): {exc}", file=sys.stderr)
            continue
        items = doc if isinstance(doc, list) else (doc.get("extensions") or doc.get("items") or [])
        for item in items:
            ident = item.get("id") or item.get("name")
            if ident:
                entries.setdefault(str(ident), item)
    return entries


def main() -> int:
    from SpotiFLAC.extensions.manager import ExtensionManager

    manager = ExtensionManager(auto_install_downloads=False)
    declared = registry_entries()

    records: list[dict] = []
    for ext in sorted(manager.list_installed(), key=lambda e: e.name):
        try:
            entry_sha = hashlib.sha256(pathlib.Path(ext.index_js).read_bytes()).hexdigest()
        except Exception as exc:  # noqa: BLE001
            print(f"cannot hash {ext.name}: {exc}", file=sys.stderr)
            entry_sha = ""
        entry = declared.get(ext.name, {})
        records.append(
            {
                "id": ext.name,
                "version": ext.manifest.get("version"),
                "runtime": ext.runtime,
                "download_provider": bool(ext.is_download_provider),
                "entry_sha256": entry_sha,
                "archive_sha256": (str(entry.get("sha256") or "").lower() or None),
                "source": entry.get("download_url"),
            }
        )

    if not records:
        print("no extension installed - nothing to record", file=sys.stderr)
        return 1

    OUT.write_text(json.dumps(records, indent=1, sort_keys=True) + "\n", encoding="utf-8")

    # ASCII only: this runs inside `docker build`, whose stdout encoding is not
    # ours to choose, and a decorative character that raises UnicodeEncodeError
    # would fail the build for a reason unrelated to what it is checking.
    print(f"{'extension':<22}{'version':<10}{'runtime':<12}{'entry sha256':<14}archive")
    for r in records:
        print(
            f"{r['id']:<22}{str(r['version']):<10}{r['runtime']:<12}"
            f"{r['entry_sha256'][:12]:<14}{(r['archive_sha256'] or '-')[:12]}"
        )
    print(f"recorded {len(records)} extensions -> {OUT}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
