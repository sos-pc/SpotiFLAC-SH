"""
grant_solver.py — Turnstile challenge solver that captures the grant token.

Extends the Turnstile-Solver image (theyka/turnstile_solver) with one
additional endpoint: POST /grant. Instead of creating a fake page, this
navigates to the REAL Cloudflare challenge URL, solves the Turnstile widget
there, waits for the redirect to the callback URL, and extracts the grant
parameter from the final URL.

Run this INSTEAD of api_solver.py, or mount it as an additional script and
call it from a wrapper. It reuses the same browser pool pattern as the
original solver so both /turnstile and /grant can coexist if needed.

Usage:
    python3 grant_solver.py --headless true --host 0.0.0.0 --port 5000
"""

import os
import sys
import time
import uuid
import json
import logging
import asyncio
import argparse
from urllib.parse import urlparse, parse_qs

from quart import Quart, request, jsonify
from patchright.async_api import async_playwright


# ── Logging (same style as upstream) ────────────────────────────────────────

COLORS = {
    'MAGENTA': '\033[35m',
    'BLUE':    '\033[34m',
    'GREEN':   '\033[32m',
    'YELLOW':  '\033[33m',
    'RED':     '\033[31m',
    'RESET':   '\033[0m',
}

class CustomLogger(logging.Logger):
    @staticmethod
    def fmt(level, color, msg):
        ts = time.strftime('%H:%M:%S')
        return f"[{ts}] [{COLORS[color]}{level}{COLORS['RESET']}] -> {msg}"

    def debug(self, msg, *a, **kw):    super().debug(self.fmt('DEBUG', 'MAGENTA', msg), *a, **kw)
    def info(self, msg, *a, **kw):     super().info(self.fmt('INFO', 'BLUE', msg), *a, **kw)
    def success(self, msg, *a, **kw):  super().info(self.fmt('SUCCESS', 'GREEN', msg), *a, **kw)
    def warning(self, msg, *a, **kw):  super().warning(self.fmt('WARNING', 'YELLOW', msg), *a, **kw)
    def error(self, msg, *a, **kw):    super().error(self.fmt('ERROR', 'RED', msg), *a, **kw)

logging.setLoggerClass(CustomLogger)
logger = logging.getLogger("GrantSolver")
logger.setLevel(logging.DEBUG)
logger.addHandler(logging.StreamHandler(sys.stdout))


# ── Server ──────────────────────────────────────────────────────────────────

class GrantSolverServer:
    def __init__(self, headless: bool, useragent: str, debug: bool,
                 browser_type: str, thread: int):
        self.app = Quart(__name__)
        self.debug = debug
        self.headless = headless
        self.useragent = useragent
        self.browser_type = browser_type
        self.thread_count = thread
        self.browser_pool = asyncio.Queue()
        self.browser_args = []
        if useragent:
            self.browser_args.append(f"--user-agent={useragent}")
        self._playwright = None

        self.app.before_serving(self._startup)
        self.app.route('/grant', methods=['POST'])(self.process_grant)
        self.app.route('/health')(self.health)

    async def _startup(self):
        logger.info("Starting browser pool (%d threads, %s)",
                     self.thread_count, self.browser_type)
        self._playwright = await async_playwright().start()
        for i in range(self.thread_count):
            browser = await self._playwright.chromium.launch(
                channel=self.browser_type,
                headless=self.headless,
                args=self.browser_args,
            )
            await self.browser_pool.put((i + 1, browser))
            logger.success(f"Browser {i + 1} ready")
        logger.success(f"Pool ready ({self.browser_pool.qsize()} browsers)")

    async def _solve_and_capture(self, challenge_url: str,
                                  task_id: str) -> dict:
        """Navigate to the real challenge URL, solve Turnstile, capture grant."""
        index, browser = await self.browser_pool.get()
        context = await browser.new_context()
        page = await context.new_page()
        start = time.time()

        try:
            if self.debug:
                logger.debug(f"Browser {index}: navigating to challenge URL")

            await page.goto(challenge_url, wait_until="domcontentloaded",
                            timeout=30000)

            # Wait for the Turnstile iframe to appear — the challenge page
            # embeds it. We don't inject our own widget; we interact with the
            # one Cloudflare put there.
            try:
                frame = page.frame_locator("iframe[src*='challenges.cloudflare.com']")
                # Click the checkbox inside the iframe to trigger the challenge
                checkbox = frame.locator("#checkbox")
                await checkbox.wait_for(state="visible", timeout=15000)
                await checkbox.click()
                if self.debug:
                    logger.debug(f"Browser {index}: Turnstile checkbox clicked")
            except Exception:
                # Some challenge pages auto-start — not finding the checkbox
                # is normal for non-interactive challenges.
                if self.debug:
                    logger.debug(f"Browser {index}: no checkbox, challenge may be non-interactive")

            # Wait for the page to redirect away from the challenge domain.
            # After solving, Cloudflare redirects to the cb= URL with ?grant=...
            for attempt in range(20):
                await asyncio.sleep(1.5)
                current = page.url
                parsed = urlparse(current)
                qs = parse_qs(parsed.query)
                grant = qs.get('grant', [None])[0]
                if grant:
                    elapsed = round(time.time() - start, 3)
                    logger.success(
                        f"Browser {index}: grant captured ({grant[:12]}...) "
                        f"in {elapsed}s"
                    )
                    return {"grant": grant, "elapsed": elapsed}

                # Also detect if we landed on the callback path
                if 'session-grant' in current or 'grant=' in current:
                    # The grant might be embedded differently — try raw parse
                    for part in current.split('?', 1)[-1].split('&'):
                        if part.startswith('grant='):
                            grant_val = part.split('=', 1)[1]
                            elapsed = round(time.time() - start, 3)
                            logger.success(
                                f"Browser {index}: grant captured via raw parse "
                                f"({grant_val[:12]}...) in {elapsed}s"
                            )
                            return {"grant": grant_val, "elapsed": elapsed}

                if self.debug:
                    logger.debug(
                        f"Browser {index}: attempt {attempt+1}/20, "
                        f"url={current[:100]}"
                    )

            elapsed = round(time.time() - start, 3)
            logger.error(
                f"Browser {index}: timed out waiting for grant redirect "
                f"(last url: {page.url[:120]})"
            )
            return {"grant": "", "elapsed": elapsed, "error": "timeout"}

        except Exception as exc:
            elapsed = round(time.time() - start, 3)
            logger.error(f"Browser {index}: {exc}")
            return {"grant": "", "elapsed": elapsed, "error": str(exc)}
        finally:
            await context.close()
            await self.browser_pool.put((index, browser))

    async def process_grant(self):
        """POST /grant — solve a challenge and return the grant token."""
        try:
            body = await request.get_json(force=True, silent=True) or {}
        except Exception:
            body = {}

        challenge_url = body.get('challenge_url', '').strip()
        if not challenge_url:
            return jsonify({"error": "challenge_url is required"}), 400

        task_id = str(uuid.uuid4())
        logger.info(f"Grant request {task_id[:8]}: {challenge_url[:120]}")

        try:
            result = await self._solve_and_capture(challenge_url, task_id)
        except Exception as exc:
            logger.error(f"Grant {task_id[:8]}: fatal error: {exc}")
            return jsonify({"error": str(exc)}), 500

        if result.get('error'):
            return jsonify(result), 422
        if not result.get('grant'):
            return jsonify({"error": "grant not captured", "elapsed": result.get('elapsed', 0)}), 422

        return jsonify(result), 200

    @staticmethod
    async def health():
        return jsonify({"status": "ok"}), 200


# ── CLI ─────────────────────────────────────────────────────────────────────

def parse_args():
    p = argparse.ArgumentParser(description="Grant Solver (Turnstile → grant)")
    p.add_argument('--headless', type=lambda x: x.lower() == 'true',
                   default=False, help='Headless mode')
    p.add_argument('--useragent', type=str, default=None, help='User-Agent')
    p.add_argument('--debug', type=lambda x: x.lower() == 'true',
                   default=False, help='Debug logging')
    p.add_argument('--browser_type', type=str, default='chromium')
    p.add_argument('--thread', type=int, default=1)
    p.add_argument('--host', type=str, default='127.0.0.1')
    p.add_argument('--port', type=str, default='5000')
    return p.parse_args()


if __name__ == '__main__':
    args = parse_args()
    if args.headless and args.useragent is None:
        logger.error("--useragent is required in headless mode")
        sys.exit(1)

    server = GrantSolverServer(
        headless=args.headless,
        useragent=args.useragent,
        debug=args.debug,
        browser_type=args.browser_type,
        thread=args.thread,
    )
    server.app.run(host=args.host, port=int(args.port))
