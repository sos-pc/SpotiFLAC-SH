"""
SpotiFLAC engine shim — a thin, engine-AGNOSTIC HTTP front for the download engine.

Stable contract (does NOT name the underlying engine):
    POST /download  {spotify_url, services[], quality, out_dir, allow_fallback}
                 -> {status, file, error, log}
    GET|HEAD /health -> {status: "ok"}

Our Go service talks ONLY to this contract. The concrete engine
(BartolomeoRusso9/SpotiFLAC-Module-Version today) is named in exactly one place
below. If that upstream dies, we rewrite `_run_download()` to call a different
engine (spotbye, another downloader) and the Go side never changes. That
swappability is the durability insurance for putting the anonymous foundation on
a third-party upstream (see docs/module-engine.md §2).

Tagging is deliberately OFF (enrich_metadata / embed_lyrics = False): the Go
service re-tags at ingestion so it owns SPOTIFY_ID / genre / naming for catalog +
M3U8 consistency (see docs/module-engine.md §4).
"""
from __future__ import annotations

import asyncio
import inspect
import io
import logging
import os
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


class _QuietEngineErrors(logging.Filter):
    """Collapse the engine's error logging to one line per distinct problem.

    A single failing route produces a ~30-line traceback, and the engine logs it
    again at every retry — one unreachable Deezer host filled the container log
    with five identical mutagen stack traces. The exception *message* already
    says everything actionable ("... is not a valid FLAC file"); the frames below
    it are library internals that never change.

    So: drop the traceback, and drop a message identical to the previous one.
    Set ENGINE_LOG_TRACEBACKS=1 to get the full output back when debugging.
    """

    def __init__(self) -> None:
        super().__init__()
        self._last: str | None = None

    def filter(self, record: logging.LogRecord) -> bool:
        key = f"{record.name}:{record.getMessage()}"
        if key == self._last:
            return False
        self._last = key
        record.exc_info = None
        record.exc_text = None
        return True


_LOG_FILTER = _QuietEngineErrors()


def _quiet_engine_logs() -> None:
    """Attach the filter to whatever handlers currently exist.

    Called before each download rather than once at import: the engine
    configures logging when a client is constructed, so handlers installed after
    us would otherwise bypass the filter. Adding it twice is harmless — the
    membership check keeps it idempotent.
    """
    if os.environ.get("ENGINE_LOG_TRACEBACKS") == "1":
        return
    handlers = list(logging.root.handlers)
    handlers += list(logging.getLogger("SpotiFLAC").handlers)
    for h in handlers:
        if _LOG_FILTER not in h.filters:
            h.addFilter(_LOG_FILTER)


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


async def _prime_tidal_apis() -> None:
    """Populate the Tidal API list, which nothing upstream does for us.

    Without this, every Tidal download fails with
    `[tidal] UNAVAILABLE: no Tidal APIs configured`, four times over — once per
    quality tier, on a condition that cannot vary by tier. Traced 2026-07-30:

      * `PROVIDER_REGISTRY` maps "tidal" to the TidalProvider *class*, so the
        orchestrator constructs it directly. `TidalProvider.create()` — the async
        factory that refreshes the API list — is never called anywhere in the
        package, and neither is `prime_tidal_api_list()`.
      * A direct construction leaves `self._apis = _TIDAL_APIS_GET`, which is
        hardcoded `[]`. The gist that used to fill it (afkarxyz/2ce772b9…) is 404.
      * `get_rotated_tidal_api_list()` then raises "No cached Tidal API URLs",
        and its caller swallows that with a bare `except Exception`, falling back
        to the same empty `self._apis`.
      * The one path that *does* refresh the list is a background thread inside
        `core/metadata_enrichment.py` — which we disable with
        `enrich_metadata=False`, because tagging belongs to the Go ingestion.

    So the last branch that filled the list is one we switch off ourselves.
    Priming explicitly is the fix, and it belongs here rather than in a patch to
    upstream's source: it is a call we fail to make, not a line they got wrong.

    The list comes from their live endpoint registry, which does carry Tidal
    entries (`tidal.post` and `community.tidal`, both non-empty, checked
    2026-07-30). Best-effort on purpose: a failure here must not stop a Qobuz or
    Deezer download that never needed the list.
    """
    try:
        from SpotiFLAC.providers.tidal import prime_tidal_api_list

        # Sync in the version this fork pins, async from 1.5.6 on (they merged the
        # sync/async pair). Dispatch on the signature so the same shim works either
        # side of that bump — awaiting the sync one raises TypeError, and calling
        # the async one without awaiting does nothing at all. Both fail quietly,
        # which is the worst way for this to fail.
        if inspect.iscoroutinefunction(prime_tidal_api_list):
            await prime_tidal_api_list()
        else:
            await asyncio.to_thread(prime_tidal_api_list)
    except Exception as exc:  # noqa: BLE001 - never block a download on this
        logging.getLogger("spotiflac-engine-shim").warning(
            "could not prime the Tidal API list: %s", exc
        )


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
        # After construction, not before: the client installs its own logging
        # handlers, and a filter attached earlier would not cover them.
        _quiet_engine_logs()
        await _prime_tidal_apis()
        await client.download_track(req.spotify_url)


def _first_audio(d: pathlib.Path) -> pathlib.Path | None:
    for p in sorted(d.rglob("*")):
        if p.is_file() and p.suffix.lower() in AUDIO_EXTS:
            return p
    return None
