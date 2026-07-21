"""
grant_solver.py — Turnstile challenge solver using undetected-chromedriver.

undetected-chromedriver patches Chrome to remove automation flags
(navigator.webdriver, etc.) so Cloudflare gives a non-interactive
Turnstile challenge that auto-solves. This produces a valid token
accepted by verify.spotbye.qzz.io/verify.

Usage:
    python3 grant_solver.py --host 0.0.0.0 --port 5000
"""

import os, sys, time, uuid, json, logging, asyncio, argparse, re, shutil, subprocess
from urllib.parse import urlparse, parse_qs, quote

from quart import Quart, request, jsonify
import undetected_chromedriver as uc
from selenium.webdriver.common.by import By
from selenium.webdriver.support.ui import WebDriverWait
from selenium.webdriver.support import expected_conditions as EC


# ── Logging ─────────────────────────────────────────────────────────────────
COLORS = {
    'MAGENTA': '\033[35m', 'BLUE': '\033[34m', 'GREEN': '\033[32m',
    'YELLOW': '\033[33m', 'RED': '\033[31m', 'RESET': '\033[0m',
}
class CustomLogger(logging.Logger):
    @staticmethod
    def fmt(level, color, msg):
        ts = time.strftime('%H:%M:%S')
        return (f"[{ts}] [{COLORS[color]}{level}{COLORS['RESET']}] -> {msg}")
    def debug(self, msg, *a, **kw):
        super().debug(self.fmt('DEBUG', 'MAGENTA', msg), *a, **kw)
    def info(self, msg, *a, **kw):
        super().info(self.fmt('INFO', 'BLUE', msg), *a, **kw)
    def success(self, msg, *a, **kw):
        super().info(self.fmt('SUCCESS', 'GREEN', msg), *a, **kw)
    def warning(self, msg, *a, **kw):
        super().warning(self.fmt('WARNING', 'YELLOW', msg), *a, **kw)
    def error(self, msg, *a, **kw):
        super().error(self.fmt('ERROR', 'RED', msg), *a, **kw)
logging.setLoggerClass(CustomLogger)
logger = logging.getLogger("GrantSolver")
logger.setLevel(logging.DEBUG)
logger.addHandler(logging.StreamHandler(sys.stdout))


# ── Chrome lifecycle helpers ────────────────────────────────────────────────

def _kill_leftover_chrome():
    """Kill any leftover Chromium processes from previous crashed sessions."""
    try:
        subprocess.run(
            ["pkill", "-f", "chromium"],
            timeout=3, capture_output=True)
    except Exception:
        pass
    time.sleep(0.5)


def _clean_chrome_dirs():
    """Remove stale Chrome user data and lock files left by crashed sessions."""
    for pattern in [
        "/tmp/.com.google.Chrome*",
        "/tmp/.org.chromium.Chromium*",
    ]:
        try:
            subprocess.run(
                ["sh", "-c", f"rm -rf {pattern}"],
                timeout=3, capture_output=True)
        except Exception:
            pass


def _get_chromium_version():
    """Detect installed Chromium major version. Returns 150 as fallback."""
    try:
        out = subprocess.check_output(
            ["/usr/bin/chromium", "--version"],
            stderr=subprocess.STDOUT, timeout=5).decode()
        match = re.search(r'Chromium\s+(\d+)', out)
        return int(match.group(1)) if match else 150
    except Exception:
        return 150


# ── Solver ──────────────────────────────────────────────────────────────────

def solve_challenge(challenge_url, max_retries=3):
    """Navigate to the challenge URL with undetected Chrome, wait for
    Turnstile to auto-solve and redirect, extract the grant token.

    Retries up to max_retries times with cleanup between attempts,
    handling the "chrome not reachable" error that occurs after the
    container has been running for a while (zombie Chrome processes,
    stale lock files, Xvfb backpressure)."""

    options = uc.ChromeOptions()
    options.add_argument("--window-size=1920,1080")
    options.add_argument("--no-sandbox")
    options.add_argument("--disable-dev-shm-usage")
    options.add_argument("--disable-gpu")
    # Prevent Chrome crash handler from spawning persistent processes
    options.add_argument("--disable-crashpad")
    options.add_argument("--disable-breakpad")
    options.add_argument("--disable-crash-reporter")
    # Reduce resource footprint
    options.add_argument("--disable-features=TranslateUI")
    options.add_argument("--disable-sync")
    options.add_argument("--disable-background-networking")
    # The display is provided by Xvfb (ENV DISPLAY=:99)

    version_main = _get_chromium_version()
    logger.debug(f"Chromium version: {version_main}")

    last_error = None
    for attempt in range(max_retries):
        if attempt > 0:
            wait = 2 ** attempt  # 2s, 4s, 8s
            logger.warning(
                f"Retry {attempt + 1}/{max_retries} after {wait}s "
                f"(previous: {str(last_error)[:120]})")
            _kill_leftover_chrome()
            _clean_chrome_dirs()
            time.sleep(wait)
        else:
            # First attempt: clean any leftovers from previous sessions
            _kill_leftover_chrome()
            _clean_chrome_dirs()

        driver = None
        start = time.time()
        try:
            logger.debug("Launching undetected Chrome")
            driver = uc.Chrome(
                options=options,
                headless=False,
                use_subprocess=True,  # easier to kill cleanly than in-process
                version_main=version_main,
            )

            logger.debug("Navigating to challenge URL")
            driver.get(challenge_url)

            logger.debug("Waiting for Turnstile to solve and redirect")

            # Wait for countdown + Turnstile to load, then try to click
            time.sleep(8)
            try:
                iframe = WebDriverWait(driver, 10).until(
                    EC.presence_of_element_located(
                        (By.CSS_SELECTOR,
                         "iframe[src*='challenges.cloudflare.com']")))
                driver.switch_to.frame(iframe)
                checkbox = WebDriverWait(driver, 5).until(
                    EC.element_to_be_clickable(
                        (By.CSS_SELECTOR, "#checkbox")))
                checkbox.click()
                driver.switch_to.default_content()
                logger.debug("Turnstile checkbox clicked")
            except Exception:
                try:
                    widget = driver.find_element(
                        By.CSS_SELECTOR, ".cf-turnstile")
                    widget.click()
                    logger.debug("Turnstile inline widget clicked")
                except Exception:
                    logger.debug("No clickable Turnstile element found")

            # Poll the URL for up to 90 seconds
            for poll in range(90):
                time.sleep(1)
                current_url = driver.current_url

                parsed = urlparse(current_url)
                grant = parse_qs(parsed.query).get('grant', [None])[0]
                if grant:
                    elapsed = round(time.time() - start, 3)
                    logger.success(
                        f"grant captured ({grant[:15]}...) in {elapsed}s")
                    return {"grant": grant, "elapsed": elapsed}

                if 'grant=' in current_url:
                    for part in current_url.split('?', 1)[-1].split('&'):
                        if part.startswith('grant='):
                            g = part.split('=', 1)[1]
                            elapsed = round(time.time() - start, 3)
                            logger.success(
                                f"grant via parse ({g[:15]}...) "
                                f"in {elapsed}s")
                            return {"grant": g, "elapsed": elapsed}

                if poll % 15 == 0 and poll > 0:
                    logger.debug(
                        f"waiting {poll}s, "
                        f"current URL: {current_url[:100]}")

            # Timeout
            elapsed = round(time.time() - start, 3)
            try:
                title = driver.title
                body = driver.find_element(By.TAG_NAME, "body").text[:300]
                logger.error(
                    f"timeout after {elapsed}s "
                    f"(title='{title}', body='{body}')")
            except Exception:
                logger.error(f"timeout after {elapsed}s")
            return {"grant": "", "elapsed": elapsed, "error": "timeout"}

        except Exception as exc:
            last_error = exc
            logger.error(
                f"error (attempt {attempt + 1}/{max_retries}): {exc}")
            # Fall through to retry
        finally:
            if driver:
                try:
                    driver.quit()
                except Exception:
                    pass
            # Always clean up between sessions
            _kill_leftover_chrome()
            _clean_chrome_dirs()

    # All retries exhausted
    return {"grant": "", "elapsed": 0,
            "error": f"all {max_retries} attempts failed: {last_error}"}


# ── Musicfetch lookup (uses browser to bypass Vercel WAF) ─────────────────

def lookup_track(query: str) -> dict:
    """Query musicfetch.io from a real browser (bypasses Vercel WAF)
    and return platform links including Qobuz track URL."""
    start = time.time()

    options = uc.ChromeOptions()
    options.add_argument("--window-size=1920,1080")
    options.add_argument("--no-sandbox")
    options.add_argument("--disable-dev-shm-usage")
    options.add_argument("--disable-gpu")
    options.add_argument("--disable-crashpad")
    options.add_argument("--disable-breakpad")
    options.add_argument("--disable-crash-reporter")

    version_main = _get_chromium_version()

    driver = None
    try:
        _kill_leftover_chrome()
        _clean_chrome_dirs()

        logger.debug("Launching Chrome for Musicfetch lookup")
        driver = uc.Chrome(
            options=options,
            headless=False,
            use_subprocess=True,
            version_main=version_main,
        )

        # Navigate to musicfetch.io to establish origin and pass WAF
        logger.debug("Loading musicfetch.io to establish session")
        driver.get("https://musicfetch.io/")
        time.sleep(3)  # let the page initialize

        # Call the internal search API from the browser's JS context.
        # Uses execute_async_script because fetch() returns a Promise
        # (execute_script is synchronous and would return undefined).
        encoded_query = quote(query)
        js = (
            "var done = arguments[arguments.length - 1];"
            f"fetch('/api/musicfetch/search?query={encoded_query}&types=track')"
            "  .then(r => r.json())"
            "  .then(d => done(JSON.stringify(d)))"
            "  .catch(e => done(JSON.stringify({error: e.message})));"
        )
        logger.debug(f"Calling /api/musicfetch/search?query={query}")
        result_json = driver.execute_async_script(js)
        elapsed = round(time.time() - start, 3)

        try:
            data = json.loads(result_json)
        except Exception:
            logger.error(f"Failed to parse Musicfetch response: {str(result_json)[:300]}")
            return {"error": "invalid response", "elapsed": elapsed}

        if "error" in data:
            logger.error(f"Musicfetch error: {data}")
            return {"error": str(data.get("error")), "elapsed": elapsed}

        # Debug: log raw response structure
        logger.debug(f"Raw response keys: {list(data.keys()) if isinstance(data, dict) else type(data)}")
        if isinstance(data, dict):
            for k, v in data.items():
                if isinstance(v, list):
                    logger.debug(f"  {k}: list of {len(v)}")
                    if v:
                        logger.debug(f"    first item keys: {list(v[0].keys()) if isinstance(v[0], dict) else type(v[0])}")
                elif isinstance(v, dict):
                    logger.debug(f"  {k}: dict with keys {list(v.keys())[:10]}")
                else:
                    logger.debug(f"  {k}: {type(v).__name__}")

        # Extract results - response is {status, data: {tracks: [...]}}
        items = []
        inner = data.get("data", {})
        if isinstance(inner, dict):
            # Try common keys: tracks, items, results
            for key in ("tracks", "items", "results"):
                candidate = inner.get(key)
                if isinstance(candidate, list):
                    items = candidate
                    break
            # If no list found, treat inner values as items
            if not items:
                for v in inner.values():
                    if isinstance(v, list):
                        items = v
                        break
        elif isinstance(inner, list):
            items = inner
        if not items:
            logger.warning(f"No tracks in response. data keys: {list(inner.keys()) if isinstance(inner, dict) else 'N/A'}")
            return {"results": [], "elapsed": elapsed}

        # Log first item structure for debugging
        if items:
            first = items[0]
            logger.debug(f"First track keys: {list(first.keys()) if isinstance(first, dict) else type(first)}")

        logger.success(
            f"Musicfetch returned {len(items)} result(s) in {elapsed}s")

        # Extract platform links for each result
        results = []
        for item in items[:5]:  # limit to top 5
            entry = {
                "name": item.get("name"),
                "artist_name": item.get("artistName"),
                "isrc": item.get("isrc"),
                "duration": item.get("duration"),
                "release_year": item.get("releaseYear"),
                "image": item.get("image"),
                "platforms": {},
            }
            # Extract platform links from services
            services = item.get("services", {})
            for platform, info in services.items():
                if isinstance(info, dict):
                    entry["platforms"][platform] = info.get("link", "")
                elif isinstance(info, str):
                    entry["platforms"][platform] = info
            results.append(entry)

        return {"results": results, "elapsed": elapsed}

    except Exception as exc:
        elapsed = round(time.time() - start, 3)
        logger.error(f"Musicfetch lookup error: {exc}")
        return {"error": str(exc), "elapsed": elapsed}
    finally:
        if driver:
            try:
                driver.quit()
            except Exception:
                pass
            _kill_leftover_chrome()
            _clean_chrome_dirs()


# ── HTTP API ────────────────────────────────────────────────────────────────
app = Quart(__name__)


@app.route('/grant', methods=['POST'])
async def process_grant():
    try:
        body = await request.get_json(force=True, silent=True) or {}
    except Exception:
        body = {}
    challenge_url = body.get('challenge_url', '').strip()
    if not challenge_url:
        return jsonify({"error": "challenge_url is required"}), 400

    logger.info(f"Grant {uuid.uuid4().hex[:8]}: {challenge_url[:120]}")

    loop = asyncio.get_event_loop()
    result = await loop.run_in_executor(
        None, solve_challenge, challenge_url)

    if result.get('error'):
        return jsonify(result), 422
    if not result.get('grant'):
        return jsonify({
            "error": "grant not captured",
            "elapsed": result.get('elapsed', 0),
        }), 422
    return jsonify(result), 200


@app.route('/health')
async def health():
    return jsonify({"status": "ok"}), 200


@app.route('/lookup', methods=['POST'])
async def musicfetch_lookup():
    """Query musicfetch.io via a real browser to get platform links."""
    try:
        body = await request.get_json(force=True, silent=True) or {}
    except Exception:
        body = {}
    query = body.get('query', '').strip()
    if not query:
        return jsonify({"error": "query is required"}), 400

    logger.info(f"Musicfetch lookup {uuid.uuid4().hex[:8]}: {query[:80]}")

    loop = asyncio.get_event_loop()
    result = await loop.run_in_executor(None, lookup_track, query)

    if result.get('error'):
        return jsonify(result), 422
    return jsonify(result), 200


# ── CLI ─────────────────────────────────────────────────────────────────────
if __name__ == '__main__':
    parser = argparse.ArgumentParser(
        description="Grant Solver (undetected-chromedriver)")
    parser.add_argument('--host', type=str, default='127.0.0.1')
    parser.add_argument('--port', type=str, default='5000')
    args = parser.parse_args()
    app.run(host=args.host, port=int(args.port))
