"""
grant_solver.py — Turnstile challenge solver that captures the grant token.

Flow:
1. Parse the challenge page to extract sitekey + challenge JWT + callback URL.
2. Solve the Turnstile widget on a fake page (same technique as the original
   Turnstile-Solver: route the real URL, serve fake page, click widget).
3. POST the solved token + challenge JWT + callback URL to /verify.
4. The verify endpoint returns {success: true, callback_url: "..."}.
5. Parse the grant from callback_url.

This bypasses the 5-second countdown, the inline widget, and the redirect
chain — we talk to /verify directly with a pre-solved Turnstile token.

Usage:
    python3 grant_solver.py --headless true --host 0.0.0.0 --port 5000
"""

import os, sys, time, uuid, json, logging, asyncio, argparse, re, shutil
from urllib.parse import urlparse, parse_qs

import aiohttp
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
    HTML_TEMPLATE = """<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8">
    <meta name="viewport" content="width=device-width,initial-scale=1">
    <title>Turnstile Solver</title>
    <script src="https://challenges.cloudflare.com/turnstile/v0/api.js" async></script>
    </head><body><!-- cf turnstile --></body></html>"""

    def __init__(self, headless, useragent, debug, browser_type, thread):
        self.app = Quart(__name__)
        self.debug = debug
        self.headless = headless
        self.browser_type = browser_type
        self.thread_count = thread
        self.browser_pool = asyncio.Queue()
        self.browser_args = [
            "--disable-blink-features=AutomationControlled",
            "--disable-features=IsolateOrigins,site-per-process",
            "--no-sandbox", "--disable-setuid-sandbox",
            "--disable-dev-shm-usage", "--disable-gpu",
            "--window-size=1920,1080",
        ]
        if useragent:
            self.browser_args.append(f"--user-agent={useragent}")
        self._playwright = None
        self.app.before_serving(self._startup)
        self.app.route('/grant', methods=['POST'])(self.process_grant)
        self.app.route('/health')(self.health)

    async def _startup(self):
        logger.info("Starting browser pool (%d threads)", self.thread_count)
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

    # ── Core logic ──────────────────────────────────────────────────────────

    async def _parse_challenge_page(self, challenge_url):
        """Parse the challenge page to extract sitekey, challenge JWT, and callback URL."""
        async with aiohttp.ClientSession() as session:
            async with session.get(challenge_url,
                                   timeout=aiohttp.ClientTimeout(total=15)) as resp:
                html = await resp.text()

        sitekey = None
        challenge_jwt = None
        callback_url = None
        action = None

        # Extract sitekey from data-sitekey attribute
        m = re.search(r'data-sitekey=["\']([^"\']+)["\']', html)
        if m:
            sitekey = m.group(1)

        # Extract data-action
        m = re.search(r'data-action=["\']([^"\']+)["\']', html)
        if m:
            action = m.group(1)

        # Extract the challenge JWT from the JS variable
        m = re.search(r'const\s+challenge\s*=\s*["\']([^"\']+)["\']', html)
        if m:
            challenge_jwt = m.group(1)

        # Extract the callback URL
        m = re.search(r'const\s+callback\s*=\s*["\']([^"\']+)["\']', html)
        if m:
            callback_url = m.group(1)

        return sitekey, challenge_jwt, callback_url, action

    async def _solve_turnstile(self, challenge_url, sitekey, action, index):
        """Solve the Turnstile widget on a fake page (original solver technique)."""
        browser = None
        context = None
        page = None
        try:
            _, browser = await self.browser_pool.get()
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080})
            page = await context.new_page()

            # Build fake page with the real sitekey (and action if present)
            action_attr = f' data-action="{action}"' if action else ''
            turnstile_div = (
                f'<div class="cf-turnstile" style="background:white;"'
                f' data-sitekey="{sitekey}"{action_attr}></div>'
            )
            page_data = self.HTML_TEMPLATE.replace(
                "<!-- cf turnstile -->", turnstile_div)

            # Intercept the real URL and serve our fake page, so the widget
            # runs with the correct origin for domain validation.
            url_with_slash = challenge_url.rstrip("/") + "/"
            await page.route(url_with_slash,
                lambda route: route.fulfill(
                    body=page_data, status=200,
                    content_type="text/html"))

            await page.goto(url_with_slash, wait_until="domcontentloaded",
                            timeout=15000)

            # Make the widget visible
            await page.eval_on_selector(
                "//div[@class='cf-turnstile']",
                "el => el.style.width = '70px'")

            # Click the widget repeatedly until the response is filled
            if self.debug:
                logger.debug(f"Browser {index}: solving Turnstile")
            for attempt in range(30):
                try:
                    token = await page.input_value(
                        "[name=cf-turnstile-response]", timeout=2000)
                    if token:
                        if self.debug:
                            logger.debug(
                                f"Browser {index}: solved attempt {attempt+1} "
                                f"({token[:12]}...)")
                        return token
                except Exception:
                    pass
                try:
                    await page.locator(
                        "//div[@class='cf-turnstile']").click(timeout=1000)
                except Exception:
                    pass
                await asyncio.sleep(0.5)

            logger.error(f"Browser {index}: could not solve Turnstile")
            return None
        finally:
            if page:
                await page.close()
            if context:
                await context.close()
            if browser:
                await self.browser_pool.put((index, browser))

    async def _post_verify(self, challenge_jwt, callback_url, token):
        """POST to /verify with the solved Turnstile token to get the grant."""
        async with aiohttp.ClientSession() as session:
            payload = {
                "challenge": challenge_jwt,
                "callback": callback_url,
                "token": token,
            }
            async with session.post(
                "https://verify.spotbye.qzz.io/verify",
                json=payload,
                timeout=aiohttp.ClientTimeout(total=15),
            ) as resp:
                result = await resp.json()
                if not result.get("success"):
                    raise Exception(
                        result.get("error", f"verify returned HTTP {resp.status}"))
                return result.get("callback_url", "")

    async def _solve_and_capture(self, challenge_url, task_id):
        """Full pipeline: parse page, solve Turnstile, POST /verify, extract grant."""
        start = time.time()

        try:
            # Step 1 — Parse the challenge page (HTTP, no browser needed)
            if self.debug:
                logger.debug("Parsing challenge page")
            sitekey, challenge_jwt, callback_url, action = \
                await self._parse_challenge_page(challenge_url)

            if not sitekey:
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "no sitekey"}
            if not challenge_jwt:
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "no challenge JWT"}
            if not callback_url:
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "no callback URL"}

            if self.debug:
                logger.debug(
                    f"sitekey={sitekey[:25]}... "
                    f"action={action} "
                    f"callback={callback_url[:40]}...")

            # Step 2 — Solve Turnstile on fake page
            token = await self._solve_turnstile(
                challenge_url, sitekey, action, 1)
            if not token:
                return {"grant": "", "elapsed": round(time.time()-start,3),
                        "error": "turnstile unsolved"}

            # Step 3 — POST /verify to get the grant URL
            if self.debug:
                logger.debug("Calling /verify")
            grant_url = await self._post_verify(
                challenge_jwt, callback_url, token)

            # Step 4 — Extract grant from the callback_url
            parsed = urlparse(grant_url)
            qs = parse_qs(parsed.query)
            grant = qs.get('grant', [None])[0]
            if grant:
                elapsed = round(time.time() - start, 3)
                logger.success(
                    f"grant captured ({grant[:12]}...) in {elapsed}s")
                return {"grant": grant, "elapsed": elapsed}

            # Fallback: parse grant= from URL directly
            if 'grant=' in grant_url:
                for part in grant_url.split('?',1)[-1].split('&'):
                    if part.startswith('grant='):
                        g = part.split('=',1)[1]
                        elapsed = round(time.time()-start,3)
                        logger.success(
                            f"grant via parse ({g[:12]}...) in {elapsed}s")
                        return {"grant": g, "elapsed": elapsed}

            return {"grant": "", "elapsed": round(time.time()-start,3),
                    "error": f"no grant in callback_url: {grant_url[:100]}"}

        except Exception as exc:
            elapsed = round(time.time() - start, 3)
            logger.error(f"{exc}")
            return {"grant": "", "elapsed": elapsed, "error": str(exc)}

    # ── HTTP endpoints ─────────────────────────────────────────────────────
    async def process_grant(self):
        try:
            body = await request.get_json(force=True, silent=True) or {}
        except Exception:
            body = {}
        challenge_url = body.get('challenge_url', '').strip()
        if not challenge_url:
            return jsonify({"error": "challenge_url is required"}), 400
        logger.info(f"Grant {uuid.uuid4().hex[:8]}: {challenge_url[:120]}")
        result = await self._solve_and_capture(challenge_url, "")
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
    p.add_argument('--headless', type=lambda x: x.lower()=='true', default=False)
    p.add_argument('--useragent', type=str, default=None)
    p.add_argument('--debug', type=lambda x: x.lower()=='true', default=False)
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
