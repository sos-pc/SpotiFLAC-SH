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


def _identity() -> dict[str, str]:
    """What this image actually is: engine version and the commit that built it."""
    try:
        import importlib.metadata as _md
        engine = _md.version("SpotiFLAC")
    except Exception:  # noqa: BLE001 — identity must never break startup
        engine = "unknown"
    rev = os.environ.get("ENGINE_REVISION") or "unknown"
    return {"engine_version": engine, "revision": rev[:7] if rev != "unknown" else rev}


def _log_identity() -> None:
    """Print it once at startup.

    Answering "is the fix I just pushed actually running?" used to mean reading
    OCI labels off the registry from another machine — labels are image
    metadata and invisible from inside the container, and a `compose pull`
    without `up -d` leaves the old container running while saying nothing about
    it. One line at startup settles it from `docker logs` alone.

    Logged on uvicorn's own logger, which is configured by the time this module
    is imported, so the line lands next to "Started server process" rather than
    into a root logger that may have no handler yet.
    """
    ident = _identity()
    # ASCII only: this is the first thing the container prints, and a locale
    # without UTF-8 stdio would turn a decorative separator into a logging
    # error where the identity line should be.
    logging.getLogger("uvicorn.error").info(
        "engine shim: SpotiFLAC %s, image %s",
        ident["engine_version"], ident["revision"],
    )


app = FastAPI(title="spotiflac-engine-shim")
_log_identity()


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
    """Filter at logger level, and drop the progress handlers upstream leaks.

    This function previously attached the filter to the handlers on `root` and
    on `SpotiFLAC`, and did nothing at all: production logs kept every traceback
    and printed every line twice. Two upstream behaviours defeat it, both in
    `SpotiFLAC/core/progress.py::install_console_interception`, which runs
    *during* the download — after anything we do here:

    1. It removes every StreamHandler from root and from `SpotiFLAC*`, so the
       handlers we just attached the filter to are destroyed moments later.
       Handler-level attachment can never survive that ordering. Logger-level
       does: a record is filtered at the logger it was logged on, before it
       reaches any handler, whatever handlers exist by then.

    2. It then adds a fresh TqdmLoggingHandler to root — and since that class
       extends logging.Handler rather than logging.StreamHandler, its own
       cleanup in (1) never removes it, and `uninstall_console_interception`
       does not either. One accumulates per download, each printing every line
       again: measured 1, 2, 3, 4, 5 handlers over five downloads. Pruning them
       here leaves exactly one, because upstream re-adds one during the
       download.

    Attaching at logger level only, never both: a logger filter that consumed
    the "same as previous message" state would make the handler pass drop the
    record entirely and silence the container.

    Loggers are enumerated rather than named: upstream creates 39 module-level
    loggers at import, all of which exist by the time this runs, plus pydoll's
    own tree, which is not under `SpotiFLAC` at all.
    """
    if os.environ.get("ENGINE_LOG_TRACEBACKS") == "1":
        return

    root = logging.getLogger()
    for h in list(root.handlers):
        if "Tqdm" in type(h).__name__:
            root.removeHandler(h)

    loggers = [root]
    loggers += [lg for lg in logging.Logger.manager.loggerDict.values()
                if isinstance(lg, logging.Logger)]
    for lg in loggers:
        if _LOG_FILTER not in lg.filters:
            lg.addFilter(_LOG_FILTER)


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
    # Identity alongside liveness: additive, so the Go side's existing decode
    # into {status} is unaffected, and `curl` from the app container answers
    # "what is actually running over there?" without restarting anything to
    # read a startup line.
    return {"status": "ok", **_identity()}


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

    # The engine can report success on a file it just failed to parse, so
    # "a file with an audio extension exists" is not enough to call this ok.
    # Checking here rather than only in Go keeps the contract honest, fails
    # before the file is streamed across the volume boundary, and leaves two
    # independent checks between a corrupt payload and the library.
    if (why := _unplayable(audio)) is not None:
        _discard(out)
        return DownloadResponse(status="error", error=why, log=log.getvalue())

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

    The keywords below are asserted at build time by contract-check.py, which
    fails the image rather than let an upstream rename reach production. It used
    to say "signature verified 2026-07-23" here; a date only records that
    someone looked once, at a version that is no longer the one being built.
    Add a keyword to this call and add it there too, or it goes unchecked.

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


def _unplayable(p: pathlib.Path) -> str | None:
    """Return why `p` is not the audio its name claims, or None if it is fine.

    An extension is not evidence. Observed in production 2026-08-04: a Deezer
    route whose stream died mid-transfer left the partial response at a .flac
    path, the engine logged "is not a valid FLAC file", downgraded it to
    "Tagging failed (non-fatal)", and still reported the download successful.
    Every consumer of this contract would have taken that on trust.

    mutagen deliberately, not ffprobe or a magic-byte check: it is what the
    engine's own tagger uses, so "mutagen cannot read this" is exactly the
    condition that made tagging fail. Same library, same verdict, no new
    dependency and no subprocess.

    Reports rather than raises so the caller can put the reason in the error
    field, where the Go side already logs it.
    """
    try:
        size = p.stat().st_size
    except OSError as e:  # noqa: BLE001
        return f"cannot stat produced file: {e}"
    if size == 0:
        return "engine produced an empty file"

    try:
        import mutagen
    except ImportError:  # pragma: no cover — mutagen ships with the engine
        return None      # never reject on our own missing dependency

    try:
        parsed = mutagen.File(str(p))
    except Exception as e:  # noqa: BLE001 — mutagen raises per-format types
        return f"produced file is not readable audio ({size} bytes): {e}"
    if parsed is None:
        return f"produced file is not recognised as audio ({size} bytes)"
    return None
