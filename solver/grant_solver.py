"""
grant_solver.py — Turnstile challenge solver using undetected-chromedriver.

undetected-chromedriver patches Chrome to remove automation flags
(navigator.webdriver, etc.) so Cloudflare gives a non-interactive
Turnstile challenge that auto-solves. This produces a valid token
accepted by verify.spotbye.qzz.io/verify.

Usage:
    python3 grant_solver.py --host 0.0.0.0 --port 5000
"""

import os, sys, time, uuid, json, logging, asyncio, argparse, re, shutil
from urllib.parse import urlparse, parse_qs
from threading import Thread

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


# ── Solver ──────────────────────────────────────────────────────────────────
def solve_challenge(challenge_url: str) -> dict:
    """Navigate to the challenge URL with undetected Chrome, wait for Turnstile
    to auto-solve and redirect, extract the grant token."""
    start = time.time()

    options = uc.ChromeOptions()
    options.add_argument("--window-size=1920,1080")
    options.add_argument("--no-sandbox")
    options.add_argument("--disable-dev-shm-usage")
    # The display is provided by Xvfb (ENV DISPLAY=:99)
    options.add_argument("--disable-gpu")

    driver = None
    try:
        logger.debug("Launching undetected Chrome")
        driver = uc.Chrome(
            options=options,
            headless=False,
            use_subprocess=False,
        )

        logger.debug("Navigating to challenge URL")
        driver.get(challenge_url)

        # Wait for the page to process: 5s countdown + Turnstile load + solve.
        # With undetected Chrome, Turnstile should auto-solve in < 30s.
        logger.debug("Waiting for Turnstile to solve and redirect")

        # Poll the URL for up to 90 seconds
        for attempt in range(90):
            time.sleep(1)
            current_url = driver.current_url

            # Check for grant in query string (redirect happened)
            parsed = urlparse(current_url)
            qs = parse_qs(parsed.query)
            grant = qs.get('grant', [None])[0]
            if grant:
                elapsed = round(time.time() - start, 3)
                logger.success(
                    f"grant captured ({grant[:15]}...) in {elapsed}s")
                return {"grant": grant, "elapsed": elapsed}

            # Also check for grant= anywhere in the URL
            if 'grant=' in current_url:
                for part in current_url.split('?', 1)[-1].split('&'):
                    if part.startswith('grant='):
                        g = part.split('=', 1)[1]
                        elapsed = round(time.time() - start, 3)
                        logger.success(
                            f"grant via parse ({g[:15]}...) in {elapsed}s")
                        return {"grant": g, "elapsed": elapsed}

            # Log progress
            if attempt % 15 == 0 and attempt > 0:
                logger.debug(
                    f"waiting {attempt}s, current URL: "
                    f"{current_url[:100]}")

        # Timeout — try to extract any useful info
        elapsed = round(time.time() - start, 3)
        try:
            page_title = driver.title
            body = driver.find_element(By.TAG_NAME, "body").text[:300]
            logger.error(
                f"timeout after {elapsed}s "
                f"(title='{page_title}', body='{body}')")
        except Exception:
            logger.error(f"timeout after {elapsed}s")

        return {"grant": "", "elapsed": elapsed, "error": "timeout"}

    except Exception as exc:
        elapsed = round(time.time() - start, 3)
        logger.error(f"error: {exc}")
        return {"grant": "", "elapsed": elapsed, "error": str(exc)}
    finally:
        if driver:
            try:
                driver.quit()
            except Exception:
                pass


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

    # Run the blocking Selenium code in a thread
    loop = asyncio.get_event_loop()
    result = await loop.run_in_executor(None, solve_challenge, challenge_url)

    if result.get('error'):
        return jsonify(result), 422
    if not result.get('grant'):
        return jsonify({"error": "grant not captured",
                        "elapsed": result.get('elapsed', 0)}), 422
    return jsonify(result), 200

@app.route('/health')
async def health():
    return jsonify({"status": "ok"}), 200


# ── CLI ─────────────────────────────────────────────────────────────────────
if __name__ == '__main__':
    parser = argparse.ArgumentParser(description="Grant Solver (undetected-chromedriver)")
    parser.add_argument('--host', type=str, default='127.0.0.1')
    parser.add_argument('--port', type=str, default='5000')
    args = parser.parse_args()
    app.run(host=args.host, port=int(args.port))
