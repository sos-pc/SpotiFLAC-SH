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

    async def _solve_fake_page_turnstile(self, challenge_url, sitekey, action):
        """Solve the Turnstile widget on a fake page (original solver technique)."""
        browser = None
        context = None
        page = None
        try:
            index, browser = await self.browser_pool.get()
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

    async def _post_verify_via_browser(self, page, challenge_jwt, callback_url, token):
        """POST /verify using the browser's fetch(), so cookies are included."""
        result = await page.evaluate(f"""
            (async () => {{
                const resp = await fetch('/verify', {{
                    method: 'POST',
                    headers: {{'Content-Type': 'application/json'}},
                    body: JSON.stringify({{
                        challenge: '{challenge_jwt}',
                        callback: '{callback_url}',
                        token: '{token}'
                    }})
                }});
                const body = await resp.text();
                return JSON.stringify({{
                    status: resp.status,
                    body: body
                }});
            }})()
        """)
        data = json.loads(result)
        if data['status'] != 200:
            raise Exception(
                f"verify HTTP {data['status']}: {data['body'][:200]}")
        parsed = json.loads(data['body'])
        if not parsed.get('success'):
            raise Exception(
                f"{parsed.get('error', 'unknown')} "
                f"(HTTP {data['status']})")
        return parsed.get('callback_url', '')

    async def _post_verify(self, challenge_jwt, callback_url, token):
        """POST to /verify (fallback when no browser context is available)."""
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
                body = await resp.text()
                try:
                    result = json.loads(body)
                except Exception:
                    raise Exception(
                        f"verify returned HTTP {resp.status}: {body[:200]}")
                if not result.get("success"):
                    raise Exception(
                        f"{result.get('error', 'unknown')} "
                        f"(HTTP {resp.status}, body: {body[:200]})")
                return result.get("callback_url", "")

    async def _solve_fake_page_turnstile_in_context(
        self, context, challenge_url, sitekey, action, index):
        """Solve Turnstile on a fake page using the given browser context."""
        page = await context.new_page()
        try:
            action_attr = f' data-action="{action}"' if action else ''
            turnstile_div = (
                f'<div class="cf-turnstile" style="background:white;"'
                f' data-sitekey="{sitekey}"{action_attr}></div>'
            )
            page_data = self.HTML_TEMPLATE.replace(
                "<!-- cf turnstile -->", turnstile_div)
            url_with_slash = challenge_url.rstrip("/") + "/"
            await page.route(url_with_slash,
                lambda route: route.fulfill(
                    body=page_data, status=200,
                    content_type="text/html"))
            await page.goto(url_with_slash, wait_until="domcontentloaded",
                            timeout=15000)
            await page.eval_on_selector(
                "//div[@class='cf-turnstile']",
                "el => el.style.width = '70px'")
            if self.debug:
                logger.debug(f"Browser {index}: solving Turnstile (in-context)")
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
            await page.close()

    @staticmethod
    def _extract_grant(grant_url, start):
        """Extract the grant token from a callback URL."""
        parsed = urlparse(grant_url)
        qs = parse_qs(parsed.query)
        grant = qs.get('grant', [None])[0]
        if grant:
            elapsed = round(time.time() - start, 3)
            logger.success(f"grant captured ({grant[:12]}...) in {elapsed}s")
            return {"grant": grant, "elapsed": elapsed}
        if 'grant=' in grant_url:
            for part in grant_url.split('?',1)[-1].split('&'):
                if part.startswith('grant='):
                    g = part.split('=',1)[1]
                    elapsed = round(time.time()-start,3)
                    logger.success(f"grant via parse ({g[:12]}...) in {elapsed}s")
                    return {"grant": g, "elapsed": elapsed}
        return {"grant": "", "elapsed": round(time.time()-start,3),
                "error": f"no grant in url: {grant_url[:100]}"}

    async def _solve_and_capture(self, challenge_url, task_id):
        """Two strategies, tried in order:

        A) Navigate to the real page, wait for Turnstile to auto-solve
           (non-interactive), capture the token from the page, POST /verify.
        B) Fallback: fake page if A times out.
        """
        start = time.time()

        # ── Strategy A: Real page ──────────────────────────────────────
        try:
            result = await self._solve_on_real_page(challenge_url, start)
            if result and result.get('grant'):
                return result
            logger.info("Real page: no auto-solve, trying fake page...")
        except Exception as e:
            logger.info(f"Real page failed ({e}), trying fake page...")

        # ── Strategy B: Fake page (with browser cookies) ─────────────────
        try:
            index, browser = await self.browser_pool.get()
            context = None
            page = None
            try:
                context = await browser.new_context(
                    viewport={"width": 1920, "height": 1080})
                page = await context.new_page()

                # Load the real page first to get session cookies
                logger.debug(f"Browser {index}: loading real page for cookies")
                await page.goto(challenge_url, wait_until="domcontentloaded",
                                timeout=30000)
                await asyncio.sleep(1)

                # Parse metadata from the loaded page
                content = await page.content()
                sitekey = None
                challenge_jwt = None
                callback_url = None
                action = None
                for pattern in [
                    r'data-sitekey=["\']([^"\']+)["\']',
                    r'["\']sitekey["\']\s*[:=]\s*["\']([^"\']{10,})["\']',
                ]:
                    m = re.search(pattern, content)
                    if m:
                        sitekey = m.group(1)
                        break
                m = re.search(r'data-action=["\']([^"\']+)["\']', content)
                if m:
                    action = m.group(1)
                m = re.search(
                    r'const\s+challenge\s*=\s*["\']([^"\']+)["\']',
                    content)
                if m:
                    challenge_jwt = m.group(1)
                m = re.search(
                    r'const\s+callback\s*=\s*["\']([^"\']+)["\']',
                    content)
                if m:
                    callback_url = m.group(1)

                if not all([sitekey, challenge_jwt, callback_url]):
                    return {"grant": "", "elapsed": round(time.time()-start,3),
                            "error": "missing page metadata"}

                # Solve Turnstile on fake page (same browser context =
                # shares cookies)
                token = await self._solve_fake_page_turnstile_in_context(
                    context, challenge_url, sitekey, action, index)
                if not token:
                    return {"grant": "", "elapsed": round(time.time()-start,3),
                            "error": "turnstile unsolved"}

                # Navigate back to the REAL page so its scripts are available
                await page.goto(challenge_url, wait_until="networkidle",
                                timeout=15000)
                await asyncio.sleep(1)

                # Call the page's own verified() function, intercepting
                # location.replace() to capture the redirect URL
                logger.debug("Calling page's verified() via browser")
                result = await page.evaluate(f"""
                    (async () => {{
                        let capturedUrl = null;
                        const origReplace = location.replace.bind(location);
                        location.replace = function(url) {{
                            capturedUrl = url;
                        }};
                        try {{
                            await verified('{token}');
                        }} catch(e) {{
                            return JSON.stringify({{error: e.message}});
                        }}
                        location.replace = origReplace;
                        return JSON.stringify({{capturedUrl: capturedUrl}});
                    }})()
                """)
                data = json.loads(result)
                if data.get('error'):
                    raise Exception(data['error'])
                if data.get('capturedUrl'):
                    return self._extract_grant(data['capturedUrl'], start)
                raise Exception('verified() did not redirect')
            finally:
                if page:
                    await page.close()
                if context:
                    await context.close()
                await self.browser_pool.put((index, browser))
        except Exception as exc:
            elapsed = round(time.time() - start, 3)
            logger.error(f"Fake page fallback: {exc}")
            return {"grant": "", "elapsed": elapsed, "error": str(exc)}

    async def _solve_on_real_page(self, challenge_url, start):
        """Navigate to the real page, wait for countdown + Turnstile.
        If Turnstile auto-solves (non-interactive), capture the token
        and POST /verify."""
        index, browser = await self.browser_pool.get()
        context = None
        page = None
        try:
            context = await browser.new_context(
                viewport={"width": 1920, "height": 1080})
            page = await context.new_page()

            logger.debug(f"Browser {index}: loading real challenge page")
            await page.goto(challenge_url, wait_until="networkidle",
                            timeout=30000)

            # Wait for the 5-second countdown + Turnstile API to load
            logger.debug(f"Browser {index}: waiting for Turnstile to load")
            await asyncio.sleep(7)

            # Poll for the Turnstile response to be filled (auto-solve)
            # with detailed DOM inspection for debugging
            token = None
            for attempt in range(20):
                await asyncio.sleep(1)
                try:
                    t = await page.input_value(
                        "[name=cf-turnstile-response]", timeout=3000)
                    if t:
                        token = t
                        logger.debug(
                            f"Browser {index}: Turnstile auto-solved "
                            f"({t[:12]}...)")
                        break
                except Exception:
                    pass

                # On first and last attempt, dump DOM state
                if attempt == 0 or attempt == 18:
                    try:
                        state = await page.evaluate("""
                            (() => {
                                const w = document.querySelector('.cf-turnstile');
                                const inp = document.querySelector('[name=cf-turnstile-response]');
                                const iframes = document.querySelectorAll('iframe');
                                const tw = typeof turnstile !== 'undefined';
                                return JSON.stringify({
                                    has_widget: !!w,
                                    widget_html: w ? w.outerHTML.substring(0,200) : 'none',
                                    has_input: !!inp,
                                    input_value: inp ? (inp.value ? inp.value.substring(0,20)+'...' : '(empty)') : 'none',
                                    iframe_count: iframes.length,
                                    iframe_srcs: Array.from(iframes).map(function(f){return f.src.substring(0,80)}),
                                    turnstile_defined: tw,
                                    body_preview: document.body ? document.body.innerText.substring(0,200) : 'no body',
                                });
                            })()
                        """)
                        logger.debug(f"Browser {index}: DOM @{attempt}s: {state}")
                    except Exception:
                        pass

            if not token:
                logger.debug(
                    f"Browser {index}: no auto-solve after 15s")
                return None

            # Read the challenge JWT and callback from the page
            challenge_jwt = await page.evaluate(
                "window.challenge || ''")
            callback_url = await page.evaluate(
                "window.callback || ''")

            if not challenge_jwt or not callback_url:
                # Fall back to parsing the HTML
                content = await page.content()
                m = re.search(
                    r'const\s+challenge\s*=\s*["\']([^"\']+)["\']',
                    content)
                if m:
                    challenge_jwt = m.group(1)
                m = re.search(
                    r'const\s+callback\s*=\s*["\']([^"\']+)["\']',
                    content)
                if m:
                    callback_url = m.group(1)

            logger.debug("Calling /verify with real-page token")
            grant_url = await self._post_verify(
                challenge_jwt, callback_url, token)
            return self._extract_grant(grant_url, start)

        finally:
            if page:
                await page.close()
            if context:
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
