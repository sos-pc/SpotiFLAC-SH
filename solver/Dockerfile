# ─────────────────────────────────────────────────────────────────────────────
# Turnstile Grant Solver — companion container for SpotiFLAC
#
# Headless Chromium + Patchright that navigates to a Cloudflare Turnstile
# challenge URL, solves it, follows the redirect, and returns the grant token.
#
# Exposes a single endpoint: POST /grant
# ─────────────────────────────────────────────────────────────────────────────
FROM python:3.12-slim-bookworm

# Chromium + deps for headless browsing. --no-install-recommends keeps the
# image reasonable (~400 MB instead of ~1 GB). The fonts are needed because
# headless Chromium still measures text for layout.
RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        chromium \
        fonts-liberation \
        fonts-noto-color-emoji \
        libasound2 \
        libatk-bridge2.0-0 \
        libatk1.0-0 \
        libcups2 \
        libdrm2 \
        libgbm1 \
        libgtk-3-0 \
        libnspr4 \
        libnss3 \
        libu2f-udev \
        libxcomposite1 \
        libxdamage1 \
        libxfixes3 \
        libxkbcommon0 \
        libxrandr2 \
        xdg-utils \
        xvfb && \
    rm -rf /var/lib/apt/lists/*

# Python deps — pinned versions from Turnstile-Solver's requirements.txt
# as of July 2026. Quart for async HTTP, Patchright for browser automation.
RUN pip install --no-cache-dir \
    quart==0.20.0 \
    patchright==1.61.2 \
    aiohttp==3.11.0 \
    hypercorn==0.18.0

# Tell Patchright where to find Chromium (the Debian package installs
# chromium to /usr/bin/chromium, not the default Playwright location).
ENV PATCHRIGHT_BROWSER_PATH=/usr/bin/chromium
ENV PLAYWRIGHT_BROWSER_PATH=/usr/bin/chromium

COPY grant_solver.py /app/grant_solver.py
WORKDIR /app

# Headless mode requires a User-Agent. Bump periodically if Cloudflare
# starts fingerprinting the old one.
ENV SOLVER_USERAGENT="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
ENV DISPLAY=:99

# Start Xvfb (virtual display) so Chrome thinks it has a real screen.
# Cloudflare gives easier (non-interactive) challenges when the browser
# has a display attached, even a virtual one.
EXPOSE 5000

CMD Xvfb :99 -screen 0 1920x1080x24 -ac +extension GLX +render & \
    sleep 1 && \
    python3 /app/grant_solver.py \
    --headless false \
    --useragent "${SOLVER_USERAGENT}" \
    --host 0.0.0.0 \
    --port 5000 \
    --browser_type chromium
