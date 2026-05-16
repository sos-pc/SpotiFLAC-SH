# ─────────────────────────────────────────────────────────────────────────────
# Stage 1 — Build frontend
# ─────────────────────────────────────────────────────────────────────────────
FROM oven/bun:1 AS frontend-builder

WORKDIR /app/frontend

COPY frontend/ .
RUN bun install --frozen-lockfile
RUN bun run build

# ─────────────────────────────────────────────────────────────────────────────
# Stage 2 — Build backend Go
# ─────────────────────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS backend-builder

WORKDIR /app

# Dépendances Go (cachées si go.mod/go.sum inchangés)
COPY go.mod go.sum ./
RUN go mod download

# Sources Go
COPY *.go ./
COPY backend/ ./backend/

# Frontend buildé (nécessaire pour l'embed)
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# Retirer les dépendances wails orphelines + compiler
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o spotiflac .

# ─────────────────────────────────────────────────────────────────────────────
# Stage 3 — Runtime
# ─────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        ca-certificates \
        tzdata && \
    rm -rf /var/lib/apt/lists/*

RUN useradd -u 1000 -m -s /bin/bash nonroot
USER nonroot
WORKDIR /home/nonroot

COPY --from=backend-builder /app/spotiflac /usr/local/bin/spotiflac

EXPOSE 6890

CMD ["/usr/local/bin/spotiflac"]
