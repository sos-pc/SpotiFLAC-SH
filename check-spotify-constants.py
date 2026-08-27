#!/usr/bin/env python3
"""Do the Spotify values that ROTATE still match upstream's?

Usage:
    ./check-spotify-constants.py          # human
    ./check-spotify-constants.py --md     # markdown bullets, for the issue body

Requires the `upstream` remote, fetched. check-upstream.sh does that first.

─── Why this exists, and why it is not a file diff ──────────────────────────

Since #112 removed the SpotFetch fallback, the native TOTP path is the ONLY
source of Spotify metadata this app has. Its correctness rests on values Spotify
rotates without warning: a TOTP secret, and six persisted-query hashes. When one
turns, nothing here fails loudly — the request comes back an error and every
fetch stops working, which the operator discovers by trying to download.

spotbye/SpotiFLAC tracks Spotify closely and is a port source we already poll
every Monday, which makes it a usable early-warning oracle for exactly these
values. On 2026-08-27 all eight matched, so the oracle is worth reading.

What it does NOT do, stated so nobody mistakes the guarantee: if Spotify rotates
and upstream has not noticed yet, this says everything is fine. It answers "have
we fallen behind a source that usually knows", not "does our path still work".
The only check that answers the second one is a real fetch.

─── Direction of the comparison ─────────────────────────────────────────────

Upstream's hashes are looked for ANYWHERE in our spotify package rather than
matched field by field. Ours are not all literals at the point of use —
searchDesktopHash is a named constant precisely because it had been copied to
three call sites — and a comparison that only understood literals would report a
false divergence for it. Presence of the exact 64-hex string is what matters.

The reverse direction (we hold a hash upstream dropped) is reported as a note,
not a divergence: an operation they stopped using is not a problem for us.
"""
from __future__ import annotations

import re
import subprocess
import sys
from pathlib import Path

# Ours: every file in the package that can hold one of these values.
OURS = (
    "backend/spotify/metadata.go",
    "backend/spotify/playlistsource.go",
    "backend/spotify/client.go",
)
UPSTREAM_META = "backend/spotify_metadata.go"
UPSTREAM_TOTP = "backend/spotify_totp.go"

HASH = re.compile(r'"operationName":\s*"([A-Za-z]+)".*?"sha256Hash":\s*"([0-9a-f]{64})"', re.S)
ANY_HASH = re.compile(r"[0-9a-f]{64}")


def upstream_file(path: str) -> str | None:
    proc = subprocess.run(
        ["git", "show", f"upstream/main:{path}"],
        capture_output=True, text=True, check=False,
    )
    return proc.stdout if proc.returncode == 0 else None


def ours_text() -> str:
    return "\n".join(
        Path(p).read_text(encoding="utf-8") for p in OURS if Path(p).exists()
    )


def check() -> tuple[list[str], list[str]]:
    """(divergences, notes)."""
    bad: list[str] = []
    notes: list[str] = []

    mine = ours_text()
    if not mine:
        return (["cannot read our own spotify package - nothing was compared"], notes)

    # ── the persisted-query hashes ───────────────────────────────────────────
    up_meta = upstream_file(UPSTREAM_META)
    if up_meta is None:
        bad.append(f"upstream no longer has `{UPSTREAM_META}` - this check is now blind")
    else:
        pairs = {}
        for op, h in HASH.findall(up_meta):
            pairs.setdefault(op, h)
        if not pairs:
            bad.append(f"no persisted-query hash found in upstream `{UPSTREAM_META}` - the shape changed")
        for op, h in sorted(pairs.items()):
            # An operation we never call is not drift, it is a feature we do not
            # have — queryTrackCreditsModal is one, and reporting it weekly would
            # be a warning nobody reads by the second week. Only the operations
            # we DO issue can rotate under us.
            if f'"{op}"' not in mine:
                notes.append(f"upstream also issues `{op}`, which we do not")
                continue
            if h not in mine:
                bad.append(f"`{op}`: upstream uses `{h[:12]}...`, which appears nowhere in our spotify package")
        theirs = set(pairs.values())
        for h in sorted(set(ANY_HASH.findall(mine)) - theirs):
            notes.append(f"we hold `{h[:12]}...`, which upstream no longer uses")

    # ── the TOTP secret and version ──────────────────────────────────────────
    up_totp = upstream_file(UPSTREAM_TOTP) or up_meta or ""
    up_secret = re.search(r'TOTPSecret\s*=\s*"([A-Z2-7]+)"', up_totp)
    up_version = re.search(r"TOTPVersion\s*=\s*(\d+)", up_totp)
    my_secret = re.search(r'secret\s*:?=\s*"([A-Z2-7]{40,})"', mine)
    my_version = re.search(r"version\s*:?=\s*(\d+)", mine)

    # Reported rather than skipped: a regex that stopped matching is exactly how
    # a check like this starts passing for the wrong reason.
    if not up_secret or not up_version:
        bad.append("could not read upstream's TOTP secret/version - the check could not run")
    elif not my_secret or not my_version:
        bad.append("could not read OUR TOTP secret/version - the check could not run")
    else:
        if up_secret.group(1) != my_secret.group(1):
            bad.append(
                f"TOTP secret differs: ours ends `...{my_secret.group(1)[-8:]}`, "
                f"upstream ends `...{up_secret.group(1)[-8:]}`"
            )
        if up_version.group(1) != my_version.group(1):
            bad.append(
                f"TOTP version differs: ours `{my_version.group(1)}`, upstream `{up_version.group(1)}`"
            )

    return bad, notes


def main() -> int:
    md = "--md" in sys.argv[1:]
    bad, notes = check()

    if md:
        for line in bad:
            print(f"- ⚠️ {line}")
        for line in notes:
            print(f"- {line}")
        return 0

    if not bad:
        print("Spotify constants: aligned with upstream (TOTP secret, version, persisted-query hashes)")
    else:
        print("Spotify constants DIVERGED from upstream:")
        for line in bad:
            print(f"  - {line}")
    for line in notes:
        print(f"  note: {line}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
