"""Runtime hook: wraps run_community_verification to add the external
solver (TURNSTILE_SOLVER_URL) fallback.

Survives upstream line shifts — only breaks if the function is renamed,
removed, or its signature changes incompatibly.  Loaded by shim.py at
import time, before any request arrives.
"""

from __future__ import annotations

import json
import logging
import os
import re
import urllib.request

logger = logging.getLogger(__name__)

_applied = False


def _apply() -> bool:
    """Wrap run_community_verification.  Returns True if applied."""
    global _applied
    if _applied:
        return True

    try:
        from SpotiFLAC.core.signed_session_desktop import (
            run_community_verification,
        )
    except ImportError:
        logger.warning(
            "solver-fallback: run_community_verification not found — hook skipped"
        )
        return False

    original = run_community_verification

    def _wrapped(record):  # type: ignore[no-untyped-def]
        try:
            return original(record)
        except RuntimeError as exc:
            msg = str(exc)
            m = re.search(r"\(challenge:\s*(https?://[^)]+)\)", msg)
            if m:
                grant = _try_external_solver(m.group(1))
                if grant:
                    return grant
            raise

    import SpotiFLAC.core.signed_session_desktop as _mod
    _mod.run_community_verification = _wrapped
    _applied = True
    logger.info("solver-fallback: hook applied")
    return True


def _try_external_solver(challenge_url: str) -> str | None:
    """Try the external solver.  Returns grant on success, None on failure."""
    solver_url = os.environ.get("TURNSTILE_SOLVER_URL", "").rstrip("/")
    if not solver_url:
        return None
    try:
        req = urllib.request.Request(
            f"{solver_url}/grant",
            data=json.dumps({"challenge_url": challenge_url}).encode(),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=45) as resp:
            data = json.loads(resp.read())
        grant: str = (data.get("grant") or "").strip()
        if grant:
            return grant
    except Exception as e:
        logger.warning(
            "solver fallback failed for community session: %s", e
        )
    return None


_apply()
