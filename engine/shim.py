"""
SpotiFLAC engine shim — a thin, engine-AGNOSTIC HTTP front for the download engine.

Stable contract (does NOT name the underlying engine):
    POST /download  {spotify_url, services[], quality, out_dir}  -> {status, file, error, log}
    GET  /health                                                 -> {status: "ok"}

Our Go service talks ONLY to this contract. The concrete engine
(BartolomeoRusso9/SpotiFLAC-Module-Version today) is named in exactly one place
below. If that upstream dies, we rewrite `_run_download()` to call a different
engine (spotbye, another downloader) and the Go side never changes. That
swappability is the durability insurance for putting the anonymous foundation on
a third-party upstream (see docs/module-version-integration.md §6).

Tagging is deliberately OFF (enrich_metadata / embed_lyrics = False): the Go
service re-tags at ingestion so it owns SPOTIFY_ID / genre / naming for catalog +
M3U8 consistency (see docs/module-engine-migration.md Q2).
"""
from __future__ import annotations

import io
import logging
import pathlib
import shutil
import uuid
from contextlib import redirect_stdout, redirect_stderr

from fastapi import FastAPI
from pydantic import BaseModel

# ── The ONE place the concrete engine is named. Swap here; contract unchanged. ──
from SpotiFLAC import AsyncSpotiFLAC  # noqa: E402

AUDIO_EXTS = {".flac", ".mp3", ".m4a", ".ogg", ".opus"}

app = FastAPI(title="spotiflac-engine-shim")


class DownloadRequest(BaseModel):
    spotify_url: str
    # Anonymous default leads with real-FLAC sources — NOT tidal-first, which is
    # previews-only without a token. Tidal is promoted by the Go side (BYOT) only
    # when a valid token exists.
    services: list[str] = ["qobuz", "deezer", "amazon"]
    quality: str = "LOSSLESS"
    out_dir: str
    # Mirrors the app's "Allow Quality Fallback" setting: whether a hi-res request
    # may settle for CD quality. Sent explicitly so a user who turned it OFF gets
    # a hi-res-or-nothing download instead of the engine's permissive default.
    allow_fallback: bool = True


class DownloadResponse(BaseModel):
    status: str                 # "ok" | "error"
    file: str | None = None
    error: str | None = None
    log: str = ""


# HEAD as well as GET: the Go status probe tries HEAD first (cheaper) and only
# falls back to GET, so a GET-only route answers its liveness check with a 405.
@app.api_route("/health", methods=["GET", "HEAD"])
def health() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/download", response_model=DownloadResponse)
async def download(req: DownloadRequest) -> DownloadResponse:
    # Per-job dir so we can unambiguously identify the produced file and so the Go
    # side can ingest + clean it. Lives under the shared /staging volume.
    out = pathlib.Path(req.out_dir) / uuid.uuid4().hex
    out.mkdir(parents=True, exist_ok=True)

    log = io.StringIO()
    try:
        with redirect_stdout(log), redirect_stderr(log):
            await _run_download(req, out)
    except Exception as e:  # noqa: BLE001 — surface ANY engine failure to Go
        _discard(out)
        return DownloadResponse(status="error", error=repr(e), log=log.getvalue())

    audio = _first_audio(out)
    if audio is None:
        _discard(out)
        return DownloadResponse(status="error", error="engine produced no audio file",
                                log=log.getvalue())
    return DownloadResponse(status="ok", file=str(audio), log=log.getvalue())


def _discard(out: pathlib.Path) -> None:
    """Delete a failed job's directory.

    On success the Go side removes it after ingesting the file, but a failure
    response carries no path, so Go never learns which directory to clean. Without
    this, every failed download leaks its directory — and whatever partial or
    invalid payload it holds — on the shared volume forever. (Observed: two
    orphaned dirs after two failed tracks, 2026-07-23.)
    """
    shutil.rmtree(out, ignore_errors=True)


async def _run_download(req: DownloadRequest, out: pathlib.Path) -> None:
    """The only engine-specific code. Rewrite this body to swap engines.

    Signature verified 2026-07-23 against AsyncSpotiFLAC.__init__: output_dir,
    services, quality, enrich_metadata, embed_lyrics all exist; the class is an
    async context manager; download_track(url) -> list[TrackMetadata].

    Also available upstream if we ever want them: qobuz_token /
    qobuz_local_api_url / tidal_custom_api (BYOT pass-through), allow_fallback
    (quality), track_max_retries, timeout_s, max_concurrent_downloads,
    use_extensions_fallback (the JS/Node route fallback — leave on).
    """
    async with AsyncSpotiFLAC(
        output_dir=str(out),
        services=req.services,
        quality=req.quality,
        allow_fallback=req.allow_fallback,
        enrich_metadata=False,   # tagging owned by Go ingestion
        embed_lyrics=False,
        log_level=logging.INFO,  # default is WARNING — too quiet for the Debug Logs bridge
    ) as client:
        await client.download_track(req.spotify_url)


def _first_audio(d: pathlib.Path) -> pathlib.Path | None:
    for p in sorted(d.rglob("*")):
        if p.is_file() and p.suffix.lower() in AUDIO_EXTS:
            return p
    return None
