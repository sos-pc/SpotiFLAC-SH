"""
grant_solver.py — Turnstile challenge solver that captures the grant token.

Navigates to the real Cloudflare Turnstile challenge URL, extracts the
sitekey, solves the Turnstile widget on a fake page (the same technique
as the original Turnstile-Solver), injects the token back into the real
page, and captures the grant from the redirect URL.

Usage:
    python3 grant_solver.py --headless true --host 0.0.0.0 --port 5000
"""

import os, sys, time, uuid, json, logging, asyncio, argparse, re, shutil
from urllib.parse import urlparse, parse_qs

from quart import Quart, request, jsonify
from patchright.async_api import async_playwright


# ── Chromium path ───────────────────────────────────────────────────────────
_CHROMIUM_PATH = os.environ.get("CHROMIUM_PATH", "")
if not _CHROMIUM_PATH:
    for candidate in ["/usr/bin/chromium", "/usr/bin/chromium-browser",
                       "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable"]:
        if shutil.which(candidate) or os.path.exists(candidate):
            _CHROMIUM_PATH = candidate
            break
if not _CHROMIUM_PATH:
    raise RuntimeError("chromium not found")


# ── Logging ─────────────────────────────────────────────────────────────────
COLORS = {
    'MAGENTA': '\033[35m', 'BLUE': '\033[34m', 'GREEN': '\033[32m',
    'YELLOW': '\033[33m', 'RED': '\033[31m', 'RESET': '\033[0m',
}
class CustomLogger(logging.Logger):
    @staticmethod
    def fmt(level, color, msg):
        ts = time.strftime('%H:%M:%S')
        return f"[{ts}] [{COLORS[color]}{level}{COLORS['RESET']}] -> {msg}"
    def debug(self, msg, *a, **kw):   super().debug(self.fmt('DEBUG','MAGENTA',msg),*a,**kw)
    def info(self, msg, *a, **kw):    super().info(self.fmt('INFO','BLUE',msg),*a,**kw)
    def success(self, msg, *a, **kw): super().info(self.fmt('SUCCESS','GREEN',msg),*a,**kw)
    def warning(self, msg, *a, **kw): super().warning(self.fmt('WARNING','YELLOW',msg),*a,**kw)
    def error(self, msg, *a, **kw):   super().error(self.fmt('ERROR','RED',msg),*a,**kw)
logging.setLoggerClass(CustomLogger)
logger = logging.getLogger("GrantSolver")
logger.setLevel(logging.DEBUG)
logger.addHandler(logging.StreamHandler(sys.stdout))


# ── Server ──────────────────────────────────────────────────────────────────
class GrantSolverServer:
    def __init__(self, headless, useragent, debug, browser_type, thread):
        self.app = Quart(__name__)
        self.debug = debug
        self.headless = headless
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
                executable_path=_CHROMIUM_PATH,
                headless=self.headless,
                args=self.browser_args,
            )
            await self.browser_pool.put((i + 1, browser))
            logger.success(f"Browser {i + 1} ready")
        logger.success(f"Pool ready ({self.browser_pool.qsize()} browsers)")

    # ── Turnstile solving ───────────────────────────────────────────────────
    async def _solve_turnstile_on_fake_page(self, context, sitekey, index):
        """Create a fake page with the Turnstile widget, solve it, return token."""
        page = await context.new_page()
        try:
            turnstile_div = (
                f'<div class="cf-turnstile" data-sitekey="{sitekey}"'
                f' style="background:white"></div>'
            )
            html = f"""<!DOCTYPE html><html><head><meta charset="utf-8">
            <script src="https://challenges.cloudflare.com/turnstile/v0/api.js"
                    async defer></script></head>
            <body>{turnstile_div}</body></html>"""

            await page.set_content(html, wait_until="domcontentloaded")
            await asyncio.sleep(2)

            # Click the widget repeatedly until the response appears
            widget = page.locator(".cf-turnstile")
            for attempt in range(15):
                try:
                    token = await page.input_value(
                        "[name=cf-turnstile-response]", timeout=2000)
                    if token:
                        if self.debug:
                            logger.debug(
                                f"Browser {index}: Turnstile solved "
                                f"({token[:12]}...) on attempt {attempt+1}")
                        return token
                except Exception:
                    pass
                try:
                    await widget.click(timeout=1000)
                except Exception:
                    pass
                await asyncio.sleep(1)

            logger.error(f"Browser {index}: could not solve Turnstile")
            return None
        finally:
            await page.close()

    async def _solve_and_capture(self, challenge_url, task_id):
        """Full pipeline: extract sitekey, solve Turnstile, inject, capture grant.

        Uses page.route() to intercept the real challenge URL and serve a fake
        Turnstile page instead. This way the widget sees the REAL domain as its
        origin, which satisfies Cloudflare's domain validation. The solved token
        is then injected into the real page via JavaScript, triggering the
        callback that causes the redirect to the grant URL.
        """
        index, browser = await self.browser_pool.get()
        context = await browser.new_context()
        page = await context.new_page()
        start = time.time()

        try:
            # Step 1 — Load challenge page briefly to extract sitekey
            if self.debug:
                logger.debug(f"Browser {index}: loading challenge page for sitekey")
            await page.goto(challenge_url, wait_until="domcontentloaded",
                            timeout=30000)
            await asyncio.sleep(1)

            sitekey = None
            for sel in [".cf-turnstile", "[data-sitekey]",
                         "iframe[src*='challenges.cloudflare.com']"]:
                try:
                    sk = await page.locator(sel).first.get_attribute(
                        "data-sitekey", timeout=3000)
                    if sk:
                        sitekey = sk
                        break
                except Exception:
                    continue
            if not sitekey:
                content = await page.content()
                m = re.search(
                    r'["\']?sitekey["\']?\s*[:=]\s*["\']([^"\']{10,})["\']',
                    content)
                if m:
                    sitekey = m.group(1)
            if not sitekey:
                logger.error(f"Browser {index}: no sitekey found")
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "no sitekey"}

            if self.debug:
                logger.debug(f"Browser {index}: sitekey={sitekey[:20]}...")

            # Step 2 — Solve Turnstile by intercepting the real URL and serving
            # a fake page. The widget runs with the REAL domain as its origin,
            # so Cloudflare accepts the token as domain-valid.
            turnstile_div = (
                f'<div class="cf-turnstile" data-sitekey="{sitekey}"' +
                f' style="background:white;width:70px"></div>'
            )
            fake_html = f"""<!DOCTYPE html><html><head><meta charset="utf-8">
            <script src="https://challenges.cloudflare.com/turnstile/v0/api.js"
                    async defer></script></head>
            <body style="background:white">{turnstile_div}</body></html>"""

            # Intercept: navigating to the challenge URL returns our fake page
            url_with_slash = challenge_url.rstrip("/") + "/"
            await page.route(url_with_slash,
                lambda route: route.fulfill(
                    body=fake_html, status=200,
                    content_type="text/html"))
            await page.route(challenge_url,
                lambda route: route.fulfill(
                    body=fake_html, status=200,
                    content_type="text/html"))

            # Navigate to the real URL → gets our fake page with real origin
            await page.goto(challenge_url, wait_until="domcontentloaded",
                            timeout=15000)
            await asyncio.sleep(2)

            # Click the widget repeatedly until the response appears
            if self.debug:
                logger.debug(f"Browser {index}: solving Turnstile")
            token = None
            widget = page.locator(".cf-turnstile")
            for attempt in range(20):
                try:
                    t = await page.input_value(
                        "[name=cf-turnstile-response]", timeout=2000)
                    if t:
                        token = t
                        if self.debug:
                            logger.debug(
                                f"Browser {index}: solved on attempt "
                                f"{attempt+1} ({t[:12]}...)")
                        break
                except Exception:
                    pass
                try:
                    await widget.click(timeout=1000)
                except Exception:
                    pass
                await asyncio.sleep(1)

            # Remove the route so we can navigate to the real page
            await page.unroute(url_with_slash)
            await page.unroute(challenge_url)

            if not token:
                logger.error(f"Browser {index}: could not solve Turnstile")
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "turnstile unsolved"}

            # Step 3 — Navigate to the REAL page and inject the token
            if self.debug:
                logger.debug(f"Browser {index}: loading real page, injecting token")
            await page.goto(challenge_url, wait_until="domcontentloaded",
                            timeout=15000)
            await asyncio.sleep(0.5)

            # Inject the solved token
            await page.evaluate(f"""
                (() => {{
                    const inp = document.querySelector(
                        '[name=cf-turnstile-response]');
                    if (inp) {{
                        inp.value = '{token}';
                        inp.dispatchEvent(new Event('input',
                            {{bubbles: true}}));
                        inp.dispatchEvent(new Event('change',
                            {{bubbles: true}}));
                    }}
                    const cb = window._turnstileCb
                        || window.turnstileCallback
                        || window.onTurnstileCallback;
                    if (typeof cb === 'function') cb('{token}');
                    if (typeof turnstile !== 'undefined' && turnstile.render)
                        turnstile.render = null;
                }})()
            """)
            if self.debug:
                logger.debug(f"Browser {index}: token injected, waiting redirect")

            # Step 4 — Wait for redirect with grant
            for attempt in range(60):
                await asyncio.sleep(1.5)
                current = page.url
                parsed = urlparse(current)
                qs = parse_qs(parsed.query)
                grant = qs.get('grant', [None])[0]
                if grant:
                    elapsed = round(time.time() - start, 3)
                    logger.success(
                        f"Browser {index}: grant captured ({grant[:12]}...) "
                        f"in {elapsed}s")
                    return {"grant": grant, "elapsed": elapsed}
                if 'grant=' in current:
                    for part in current.split('?',1)[-1].split('&'):
                        if part.startswith('grant='):
                            g = part.split('=',1)[1]
                            elapsed = round(time.time()-start,3)
                            logger.success(
                                f"Browser {index}: grant via parse "
                                f"({g[:12]}...) in {elapsed}s")
                            return {"grant": g, "elapsed": elapsed}
                if self.debug and attempt % 10 == 0:
                    logger.debug(f"Browser {index}: waiting {attempt+1}/60")

            elapsed = round(time.time() - start, 3)
            try:
                title = await page.title()
                body = await page.locator("body").inner_text()
                logger.error(
                    f"Browser {index}: timeout (title='{title}', "
                    f"body='{body[:200]}')")
            except Exception:
                logger.error(f"Browser {index}: timeout")
            return {"grant": "", "elapsed": elapsed, "error": "timeout"}

        except Exception as exc:
            elapsed = round(time.time() - start, 3)
            logger.error(f"Browser {index}: {exc}")
            return {"grant": "", "elapsed": elapsed, "error": str(exc)}
        finally:
            await context.close()
            await self.browser_pool.put((index, browser))

    # ── HTTP endpoints ─────────────────────────────────────────────────────
    async def process_grant(self):
        try:
            body = await request.get_json(force=True, silent=True) or {}
        except Exception:
            body = {}
        challenge_url = body.get('challenge_url', '').strip()
        if not challenge_url:
            return jsonify({"error": "challenge_url is required"}), 400
        task_id = str(uuid.uuid4())
        logger.info(f"Grant {task_id[:8]}: {challenge_url[:120]}")
        try:
            result = await self._solve_and_capture(challenge_url, task_id)
        except Exception as exc:
            logger.error(f"Grant {task_id[:8]}: fatal: {exc}")
            return jsonify({"error": str(exc)}), 500
        if result.get('error'):
            return jsonify(result), 422
        if not result.get('grant'):
            return jsonify({"error": "grant not captured",
                            "elapsed": result.get('elapsed',0)}), 422
        return jsonify(result), 200

    @staticmethod
    async def health():
        return jsonify({"status": "ok"}), 200


# ── CLI ─────────────────────────────────────────────────────────────────────
def parse_args():
    p = argparse.ArgumentParser(description="Grant Solver")
    p.add_argument('--headless', type=lambda x: x.lower()=='true',
                   default=False)
    p.add_argument('--useragent', type=str, default=None)
    p.add_argument('--debug', type=lambda x: x.lower()=='true',
                   default=False)
    p.add_argument('--browser_type', type=str, default='chromium')
    p.add_argument('--thread', type=int, default=1)
    p.add_argument('--host', type=str, default='127.0.0.1')
    p.add_argument('--port', type=str, default='5000')
    return p.parse_args()

if __name__ == '__main__':
    args = parse_args()
    if args.headless and args.useragent is None:
        logger.error("--useragent required in headless mode")
        sys.exit(1)
    server = GrantSolverServer(
        headless=args.headless, useragent=args.useragent,
        debug=args.debug, browser_type=args.browser_type,
        thread=args.thread,
    )
    server.app.run(host=args.host, port=int(args.port))
