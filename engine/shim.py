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
import io
import logging
import os
import pathlib
import shutil
import time
import uuid
from contextlib import redirect_stdout, redirect_stderr

from fastapi import FastAPI
from pydantic import BaseModel

# ── The ONE place the concrete engine is named. Swap here; contract unchanged. ──
from SpotiFLAC import AsyncSpotiFLAC  # noqa: E402
import hooks.solver_fallback  # noqa: F401 — applies runtime patches

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


# ── Provider reachability ────────────────────────────────────────────────────
#
# /health answers "is the sidecar alive". That is not the question an operator
# has when downloads fail. On 2026-08-07 it answered ok for hours while 45 of
# Qobuz's 48 mirrors were dead, Deezer's only resolver returned 403 and Amazon's
# only host refused connections — and finding that out took an hour of manual
# forensics. This endpoint is that hour, answered in one request.
#
# It calls upstream's own checker, which is the point: we do not maintain a list
# of provider endpoints, we ask the engine what it would try.

_PROVIDER_CACHE_TTL = 300.0
# Nearly 3x the 2.2 s measured on the production engine: long enough that a cold
# call returns real data, short enough that a hung provider cannot make the
# status board feel broken in the other direction.
_PROVIDER_COLD_WAIT = 6.0
_provider_cache: dict[str, object] = {}
_provider_lock = asyncio.Lock()
_provider_refreshing = False


# The service names this image can be asked for; kept in step with
# contract-check.py's REQUIRED_SERVICES, which fails the BUILD on the same
# condition this reports at runtime. Both exist, and they are not redundant:
# the build check stops a broken image being published, this one explains an
# image that is already deployed.
_EXPECTED_SERVICES = ("qobuz", "deezer", "amazon", "tidal")


def _missing_providers(services: list[str] | None = None) -> list[str]:
    """Service names with no installed extension behind them.

    Empty is the healthy answer. Anything here means a download naming that
    service fails with `No valid providers found`, which reads like a caller
    mistake and is not one.

    Never raises: a status endpoint that 500s tells an operator less than one
    which admits it could not check.
    """
    try:
        from SpotiFLAC.extensions.catalog import extension_id
        from SpotiFLAC.extensions.manager import ExtensionManager

        manager = ExtensionManager(auto_install_downloads=False)
        missing = []
        for service in (services or _EXPECTED_SERVICES):
            ext_id = extension_id(service, manager)
            if not ext_id or not manager.get_installed(ext_id):
                missing.append(service)
        return missing
    except Exception:  # noqa: BLE001 - never fatal for a status route
        return []


def _provider_status(services: list[str]) -> dict:
    """What this image can still say about the providers it delegates to.

    Reachability used to be the answer. Upstream published an endpoint table and
    health_check.py probed all ~51 of them, which is how the status board could
    say "Qobuz: 3 of 48 reachable" instead of "the engine is up" — on 2026-08-07
    that distinction was an hour of manual forensics, answered on page load.

    SpotiFLAC 3.0.7 deleted it. `run_health_check_with_extensions` is gone;
    `run_health_check` survives and now probes *lyrics* servers, so asking it
    about "qobuz" or "tidal" matches nothing at all and asking it about "deezer"
    answers about a lyrics endpoint. Wiring that up would have produced a board
    that looks authoritative and describes something else — worse than a board
    with fewer rows, and precisely the failure this endpoint was built to end.

    What remains is answerable without upstream's help, and it is the question
    behind the 2026-08-15 incident: is there an installed extension behind each
    service this image would be asked for? A name with none is why a download
    fails with `No valid providers found`.
    """
    missing = _missing_providers(services)
    return {
        "checked_at": int(time.time()),
        # The reachability map is deliberately absent rather than empty: the Go
        # side renders one row per entry, and an empty map means "no rows",
        # which is what it already does for an engine too old to answer.
        "missing_providers": missing,
        "extensions": {
            "ok": not missing,
            "detail": "" if not missing else "no extension installed for: " + ", ".join(missing),
        },
    }


async def _refresh_providers(services: list[str]) -> None:
    global _provider_refreshing
    try:
        # No await left in here: what this reports is a local lookup now, not a
        # fleet of probes. The stale-while-revalidate machinery below is kept
        # rather than unwound in the same change that repairs the build — it
        # costs nothing but a comment saying it is now oversized for the job.
        _provider_cache["data"] = _provider_status(services)
        _provider_cache["at"] = time.monotonic()
    except Exception as exc:  # noqa: BLE001 — a diagnostic must not take the engine down
        _provider_cache["data"] = {"error": f"{type(exc).__name__}: {exc}"[:200]}
        _provider_cache["at"] = time.monotonic()
    finally:
        _provider_refreshing = False


@app.get("/providers/health")
async def providers_health(services: str = "qobuz,deezer,amazon,tidal") -> dict:
    """Per-provider reachability. Stale-while-revalidate, except when cold.

    A stale answer is served immediately and refreshed behind the request, so a
    warm call never blocks. A *cold* call waits, briefly, because the
    alternative was measurably worse: the first status page load after a deploy
    showed no provider rows at all and cached that emptiness for 30 s, which
    reads as "the feature does not work" — that is exactly how it was reported
    the first time it shipped.

    The wait is affordable because the check is not slow. Measured on the
    production engine 2026-08-08: 2.2 s for 13 real probes. The ten seconds this
    was originally built around was the cost of importing SpotiFLAC in a
    throwaway `docker exec`, not the probes — an assumption taken from a
    stopwatch on the wrong thing.
    """
    global _provider_refreshing
    wanted = [s.strip() for s in services.split(",") if s.strip()]

    def _read() -> tuple[object, bool]:
        data = _provider_cache.get("data")
        at = _provider_cache.get("at")
        return data, at is not None and (time.monotonic() - float(at)) < _PROVIDER_CACHE_TTL

    cached, fresh = _read()

    if not fresh:
        async with _provider_lock:
            if not _provider_refreshing:
                _provider_refreshing = True
                asyncio.create_task(_refresh_providers(wanted))

    if cached is None:
        # Poll the cache rather than await the task: another request may already
        # own the refresh, in which case there is no handle to await here.
        deadline = time.monotonic() + _PROVIDER_COLD_WAIT
        while time.monotonic() < deadline:
            await asyncio.sleep(0.1)
            cached, fresh = _read()
            if cached is not None:
                break

    if cached is None:
        return {"pending": True, "checked_at": None, "providers": {}}
    return {"pending": not fresh, **cached}  # type: ignore[dict-item]


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
