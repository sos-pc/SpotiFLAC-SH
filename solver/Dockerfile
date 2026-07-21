FROM python:3.11-slim-bookworm

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        chromium \
        chromium-common \
        chromium-sandbox \
        curl \
        fonts-liberation \
        libasound2 libatk-bridge2.0-0 libatk1.0-0 libcups2 libdrm2 \
        libgbm1 libgtk-3-0 libnspr4 libnss3 libu2f-udev \
        libxcomposite1 libxdamage1 libxfixes3 libxkbcommon0 libxrandr2 \
        xdg-utils xvfb procps && \
    rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir \
    quart==0.20.0 \
    hypercorn==0.18.0 \
    undetected-chromedriver \
    selenium

ENV DISPLAY=:99
ENV CHROMIUM_PATH=/usr/bin/chromium

COPY grant_solver.py /app/grant_solver.py
WORKDIR /app

# Health check: verify Quart is listening
HEALTHCHECK --interval=60s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -sf http://localhost:5000/health || exit 1

EXPOSE 5000

# Entrypoint: clean stale Xvfb lock (from previous container restart), start
# Xvfb, wait for it, then launch the solver.
CMD rm -f /tmp/.X99-lock /tmp/.X11-unix/X99 && \
    Xvfb :99 -screen 0 1920x1080x24 -ac +extension GLX +render & \
    sleep 2 && \
    python3 /app/grant_solver.py \
    --host 0.0.0.0 \
    --port 5000
