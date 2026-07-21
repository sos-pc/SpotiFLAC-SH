FROM python:3.12-slim-bookworm

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        chromium \
        fonts-liberation \
        libasound2 libatk-bridge2.0-0 libatk1.0-0 libcups2 libdrm2 \
        libgbm1 libgtk-3-0 libnspr4 libnss3 libu2f-udev \
        libxcomposite1 libxdamage1 libxfixes3 libxkbcommon0 libxrandr2 \
        xdg-utils xvfb && \
    rm -rf /var/lib/apt/lists/*

RUN pip install --no-cache-dir \
    quart==0.20.0 \
    hypercorn==0.18.0 \
    undetected-chromedriver \
    selenium

ENV DISPLAY=:99

COPY grant_solver.py /app/grant_solver.py
WORKDIR /app

EXPOSE 5000

CMD Xvfb :99 -screen 0 1920x1080x24 -ac +extension GLX +render & \
    sleep 2 && \
    python3 /app/grant_solver.py \
    --host 0.0.0.0 \
    --port 5000
