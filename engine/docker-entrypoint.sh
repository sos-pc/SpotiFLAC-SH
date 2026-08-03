#!/usr/bin/env sh
# Start the virtual display, then hand over to the CMD (uvicorn).
#
# Ported from upstream's docker-entrypoint.sh. Their image sets DISPLAY=:99 and
# starts Xvfb here; ours did neither, so every Chromium/pydoll route in the
# engine died at `[Errno 2] No such file or directory: 'Xvfb'`.
#
# Note SpotiFLAC.core.solver._ensure_xvfb() no-ops when DISPLAY is already set,
# so with this in place the engine never tries to spawn its own — which is the
# arrangement upstream designed for.
set -e

# A container restart can leave these behind and Xvfb then refuses :99.
rm -f /tmp/.X99-lock 2>/dev/null || true
rm -f /tmp/.X11-unix/X99 2>/dev/null || true

Xvfb :99 -screen 0 1280x900x24 -ac +extension GLX +render -noreset &

# Upstream sleeps 1s flat. Poll instead: usually ready in well under that, and
# on a loaded host a fixed second can be too short. Never block startup for
# long — the health endpoint has to come up either way, and nothing touches the
# display until a download request arrives.
i=0
while [ "$i" -lt 50 ]; do
    [ -e /tmp/.X11-unix/X99 ] && break
    i=$((i + 1))
    sleep 0.1
done

exec "$@"
