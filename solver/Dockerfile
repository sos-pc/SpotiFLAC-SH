# ─────────────────────────────────────────────────────────────────────────────
# Turnstile-Solver + Grant capture endpoint
#
# Extends theyka/turnstile_solver with grant_solver.py, which navigates to the
# real Cloudflare challenge URL (not a fake page), solves the Turnstile widget,
# waits for the redirect to the callback URL, and returns the grant token.
#
# This is a companion container to SpotiFLAC. The two communicate over HTTP on
# the shared Docker network; the solver never needs to be exposed to the host.
# ─────────────────────────────────────────────────────────────────────────────
FROM theyka/turnstile_solver:latest

COPY grant_solver.py /app/grant_solver.py

# Headless mode requires a User-Agent. This is a recent Chrome on Linux UA;
# bump the version periodically when the solver logs show UA-related blocks.
ENV SOLVER_USERAGENT="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

CMD python3 /app/grant_solver.py \
    --headless true \
    --useragent "${SOLVER_USERAGENT}" \
    --host 0.0.0.0 \
    --port 5000 \
    --browser_type chromium \
    --debug false
