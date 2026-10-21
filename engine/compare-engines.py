#!/usr/bin/env python3
"""Run the same real downloads through an engine and report what came out.

Made for comparing two engine images before switching production from one to
the other — the SpotiFLAC -> mornye move was the first. A green build proves an
image installs, imports, starts its bundles and finds every host method they
call; it has never proved that a track comes out the other end, and this
project has been told "healthy" by a sidecar that downloaded nothing before.

Run it INSIDE an engine container, once per engine, with the same tracks:

    docker exec spotiflac-engine        python3 /tmp/compare-engines.py --label current
    docker exec spotiflac-engine-mornye python3 /tmp/compare-engines.py --label mornye

Each run talks to its own shim on 127.0.0.1 and inspects the files on its own
disk, so nothing has to cross between containers. `--jsonl` writes one record
per attempt; `--compare A.jsonl B.jsonl` prints the two side by side.

Uses only the standard library, plus mutagen when present (both engine images
ship it) to read what the file actually is rather than what its name says.
Every downloaded file is deleted once inspected unless --keep is given.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import shutil
import statistics
import sys
import time
import urllib.error
import urllib.request

# Well-known tracks only: this file is public, and a list taken from someone's
# library would publish their listening. Pass your own with --tracks.
# Bohemian Rhapsody is deliberately included: on 2026-09-23 the Qobuz resolver
# answered 404 for it on every engine, so it shows what "not found" looks like.
DEFAULT_TRACKS = [
    "69kOkLUCkxIZYexIgSG8rq",  # Daft Punk - Get Lucky
    "0VjIjW4GlUZAMYd2vXMi3b",  # The Weeknd - Blinding Lights
    "4u7EnebtmKWzUH433cf5Qv",  # Queen - Bohemian Rhapsody (Remastered 2011)
]

# Failure causes, checked in order against the error and the engine's log.
# "session" is listed first and kept apart on purpose: obtaining a signed session
# is the part of the path most likely to differ between two images (a built-in
# browser versus an external solver), and it must not be mistaken for a provider
# that simply lacks the track.
CAUSES = [
    ("session", r"verification|challenge|turnstile|signed.?session|session expired|"
                r"bootstrap|session.?grant"),
    ("not_found", r"\b404\b|not found|no match|different track|not available|"
                  r"no .*download url"),
    ("network", r"timed? ?out|timeout|connection|temporarily unavailable|\b50[234]\b"),
]


def classify(text: str) -> str:
    low = text.lower()
    for name, pattern in CAUSES:
        if re.search(pattern, low):
            return name
    return "other"


def get_json(url: str, timeout: float = 30) -> dict:
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return json.load(resp)


def post_json(url: str, payload: dict, timeout: float) -> dict:
    req = urllib.request.Request(url, data=json.dumps(payload).encode(),
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def inspect_audio(path: pathlib.Path) -> dict:
    facts = {"bytes": path.stat().st_size, "ext": path.suffix.lower().lstrip(".")}
    try:
        import mutagen
    except ImportError:
        return facts
    try:
        parsed = mutagen.File(str(path))
    except Exception as exc:  # noqa: BLE001 - an unreadable file IS the finding
        facts["unreadable"] = f"{type(exc).__name__}: {exc}"[:120]
        return facts
    if parsed is None:
        facts["unreadable"] = "not recognised as audio"
        return facts
    info = parsed.info
    facts.update({
        "format": type(parsed).__name__,
        "sample_rate": getattr(info, "sample_rate", None),
        "bits": getattr(info, "bits_per_sample", None),
        "seconds": round(float(getattr(info, "length", 0) or 0), 1),
    })
    return facts


def run(args: argparse.Namespace) -> int:
    base = args.engine.rstrip("/")
    try:
        ident = get_json(base + "/health")
    except Exception as exc:  # noqa: BLE001
        print(f"engine not reachable at {base}: {exc}", file=sys.stderr)
        return 2
    engine = f"{ident.get('engine', '?')} {ident.get('engine_version', '?')} @ {ident.get('revision', '?')}"
    print(f"# {args.label}: {engine}")

    tracks = [t.strip() for t in args.tracks.split(",") if t.strip()]
    services = [s.strip() for s in args.services.split(",") if s.strip()]
    out_root = pathlib.Path(args.out) / args.label
    out_root.mkdir(parents=True, exist_ok=True)
    sink = open(args.jsonl, "a", encoding="utf-8") if args.jsonl else None

    records = []
    print(f"{'service':<8} {'track':<24} {'result':<10} {'secs':>6}  detail")
    for service in services:
        for track in tracks:
            started = time.monotonic()
            record = {"label": args.label, "engine": engine, "service": service,
                      "track": track}
            try:
                resp = post_json(base + "/download", {
                    "spotify_url": f"https://open.spotify.com/track/{track}",
                    "services": [service],
                    "quality": args.quality,
                    "out_dir": str(out_root),
                    "allow_fallback": True,
                }, timeout=args.timeout)
            except (urllib.error.URLError, TimeoutError, OSError) as exc:
                resp = {"status": "error", "error": f"request failed: {exc}", "log": ""}
            record["secs"] = round(time.monotonic() - started, 1)

            if resp.get("status") == "ok" and resp.get("file"):
                path = pathlib.Path(resp["file"])
                record["result"] = "ok"
                record.update(inspect_audio(path) if path.exists() else {"missing": True})
                if not args.keep:
                    shutil.rmtree(path.parent, ignore_errors=True)
                detail = (f"{record.get('format', record.get('ext'))} "
                          f"{record.get('bits') or '?'}bit/{record.get('sample_rate') or '?'}Hz "
                          f"{record.get('seconds', '?')}s {record.get('bytes', 0) // 1_000_000}MB")
                if record.get("unreadable"):
                    record["result"] = "bad_file"
                    detail = record["unreadable"]
            else:
                text = f"{resp.get('error') or ''}\n{resp.get('log') or ''}"
                record["result"] = classify(text)
                record["error"] = (resp.get("error") or "")[:200]
                detail = record["error"][:90]
            records.append(record)
            if sink:
                sink.write(json.dumps(record) + "\n")
                sink.flush()
            print(f"{service:<8} {track:<24} {record['result']:<10} {record['secs']:>6}  {detail}")
            time.sleep(args.pause)

    if sink:
        sink.close()
    if not args.keep:
        shutil.rmtree(out_root, ignore_errors=True)

    print()
    for service in services:
        rows = [r for r in records if r["service"] == service]
        ok = [r for r in rows if r["result"] == "ok"]
        causes = {}
        for r in rows:
            if r["result"] != "ok":
                causes[r["result"]] = causes.get(r["result"], 0) + 1
        median = f"{statistics.median(r['secs'] for r in ok):.1f}s" if ok else "-"
        print(f"{service:<8} {len(ok)}/{len(rows)} ok, median {median}"
              + (f", failures: {causes}" if causes else ""))
    return 0


def compare(paths: list[str]) -> int:
    by_key: dict[tuple[str, str], dict[str, dict]] = {}
    labels: list[str] = []
    for p in paths:
        for line in open(p, encoding="utf-8"):
            r = json.loads(line)
            if r["label"] not in labels:
                labels.append(r["label"])
            by_key.setdefault((r["service"], r["track"]), {})[r["label"]] = r

    def cell(r: dict | None) -> str:
        if r is None:
            return "-"
        if r["result"] == "ok":
            return f"ok {r['secs']}s {r.get('bits') or '?'}/{r.get('sample_rate') or '?'} {r.get('seconds', '?')}s"
        return f"{r['result']} {r['secs']}s"

    print(f"{'service':<8} {'track':<24} " + " ".join(f"{label:<32}" for label in labels))
    for (service, track), per in sorted(by_key.items()):
        print(f"{service:<8} {track:<24} " + " ".join(f"{cell(per.get(label)):<32}" for label in labels))
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--engine", default="http://127.0.0.1:8080")
    parser.add_argument("--label", default="engine")
    parser.add_argument("--tracks", default=",".join(DEFAULT_TRACKS),
                        help="comma-separated Spotify track ids")
    parser.add_argument("--services", default="qobuz,tidal")
    parser.add_argument("--quality", default="LOSSLESS")
    parser.add_argument("--out", default="/tmp/compare-engines")
    parser.add_argument("--jsonl", help="append one JSON record per attempt here")
    parser.add_argument("--timeout", type=float, default=600)
    parser.add_argument("--pause", type=float, default=2.0,
                        help="seconds between requests; the resolvers are shared")
    parser.add_argument("--keep", action="store_true", help="keep downloaded files")
    parser.add_argument("--compare", nargs="+", metavar="JSONL",
                        help="print saved runs side by side instead of downloading")
    args = parser.parse_args()
    return compare(args.compare) if args.compare else run(args)


if __name__ == "__main__":
    raise SystemExit(main())
