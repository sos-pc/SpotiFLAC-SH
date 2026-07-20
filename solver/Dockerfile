# ─────────────────────────────────────────────────────────────────────────────
# Turnstile Grant Solver — companion container for SpotiFLAC
#
# Based on theyka/turnstile_solver which provides X11 desktop + Chromium +
# Patchright in headed mode (not headless). Headed Chrome with a real display
# receives easier (non-interactive) Turnstile challenges from Cloudflare.
#
# Our grant_solver.py extends this with a /grant endpoint that captures the
# verification grant token by parsing the challenge page, solving Turnstile,
# and POSTing to /verify via the browser's own fetch().
# ─────────────────────────────────────────────────────────────────────────────
FROM theyka/turnstile_solver:latest

# The original image starts XRDP + desktop. We don't need RDP — we run our
# Quart server directly, relying on the existing X11 display for Chrome.
COPY grant_solver.py /app/grant_solver.py
WORKDIR /app

# Override the original CMD (which starts XRDP). We start our server.
# Chrome runs in headed mode (--headless false) with the existing X11 display.
ENV DISPLAY=:1
ENV SOLVER_USERAGENT="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

EXPOSE 5000

# Clear the inherited ENTRYPOINT from theyka/turnstile_solver (which runs
# a setup script + XRDP). We run our own server directly.
ENTRYPOINT []

# Start Xvfb (virtual display) then run our solver.
# The original image has X11 libraries but our CMD overrides the desktop.
CMD Xvfb :99 -screen 0 1920x1080x24 -ac +extension GLX +render & \
    sleep 2 && \
    python3 /app/grant_solver.py \
    --headless false \
    --useragent "${SOLVER_USERAGENT}" \
    --host 0.0.0.0 \
    --port 5000 \
    --browser_type chromium
