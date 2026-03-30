# Troubleshooting

---

## Login / Auth

### "Invalid credentials" on correct Jellyfin password

- Verify `JELLYFIN_URL` is reachable **from inside the container**: `docker exec spotiflac curl -s $JELLYFIN_URL/health`
- Jellyfin must be running and accessible. The URL should not point to `localhost` on the host machine — use the Docker host IP or a shared Docker network.

### 429 on login

SpotiFLAC rate-limits login to 5 failed attempts per 5 minutes per IP. Wait 5 minutes. If you're behind a reverse proxy, make sure it forwards `X-Forwarded-For` so the limit applies per real IP and not per proxy IP.

### Token expired loop / constant 401

- The JWT lifetime is 24 hours. If the clock on the server drifts significantly, tokens may expire early or never be accepted. Check `docker exec spotiflac date`.

---

## Downloads

### All downloads fail immediately

1. Check **Settings → APIs** status dashboard. If Tidal proxies are all red, the community pool may be temporarily down.
2. Try manually triggering a download — the error message in the queue detail explains the exact failure.
3. Try switching `service` to `qobuz` or `amazon` in Settings.

### "Song.link rate limited" / no Tidal/Qobuz link found

Song.link's free API is aggressively rate-limited. SpotiFLAC automatically falls back to scraping the Song.link HTML page, which has a much higher limit. If both fail:
- Wait a few minutes and retry.
- Check the APIs status dashboard (`Settings → APIs`).

### Downloaded file is 0 bytes or corrupt

- Usually a CDN/proxy issue — the proxy returned an error body instead of the audio file. Retry the download.
- Check if the proxy is still up in the APIs status dashboard.
- If a specific proxy is always failing, remove it in **Settings → APIs → Proxy Configuration**.

### "all Tidal proxies failed"

All community HiFi proxies are down or unreachable. Options:
1. Wait and retry — community proxies are maintained by volunteers and may have temporary downtime.
2. Authenticate with your own Tidal Premium account (see [tidal-auth.md](tidal-auth.md)) to bypass community proxies entirely.
3. Switch service to Qobuz or Amazon in Settings.

### Track not available on any platform

Some tracks are not available in lossless on Tidal/Qobuz/Amazon/Deezer (exclusives, regional restrictions, etc.). SpotiFLAC can only download what the platforms have. The job will be marked as failed.

---

## Watchlists

### Watchlist not syncing

- Check the watchlist interval — syncs run at most once per `interval_hours`.
- Trigger a manual sync via the Sync button to test immediately.
- Look at the sync history for error messages.

### Stats show incorrect numbers

- Stats are recalculated on each sync. Run a manual sync to refresh them.
- "Missing" = total Spotify tracks − tracks confirmed on disk. If you moved files, SpotiFLAC won't know until it checks again.

### `sync_deletions` deleted a file it shouldn't have

- This should not happen if multi-playlist protection is working correctly.
- If a file is referenced by multiple watchlists pointing to the same `output_dir`, it will be protected.
- If watchlists use different `output_dir` paths pointing to the same underlying folder (e.g. via symlinks), protection won't detect the overlap — use identical path strings.

---

## FFmpeg

### "ffmpeg not found" error

SpotiFLAC bundles FFmpeg in the Docker image — this error should not appear in Docker deployments.

If running from source:
- Install FFmpeg system-wide (`apt install ffmpeg` / `brew install ffmpeg`), or
- Use the auto-install feature: **Settings → System → Install FFmpeg** — SpotiFLAC will download the correct binary for your OS.

### FFmpeg decryption fails (Amazon Music)

Amazon tracks are delivered as encrypted `.m4a` files. Decryption uses FFmpeg with the `-decryption_key` flag. If it fails:
- Ensure FFmpeg version ≥ 4.4.
- Check the tail of the FFmpeg output in the error message for details.

---

## Performance

### Downloads are slow

- SpotiFLAC processes **one download at a time** by design — this avoids hammering community proxies and getting IP-banned.
- Large playlists (100+ tracks) will take time. Use watchlists to spread the load over multiple sync intervals.

### UI feels slow / SSE not connecting

- The download queue uses Server-Sent Events (`/api/v1/jobs/stream`). Make sure your reverse proxy is not buffering the response (see [deployment.md](deployment.md)).
- If using Nginx, verify `proxy_buffering off` is set on the location block.

---

## Docker

### Permission denied on volume

```bash
# Fix ownership on the host
chown -R 65532:65532 /path/to/config /path/to/music
```

The container runs as `nonroot` (uid 65532).

### Container exits immediately on startup

```bash
docker compose logs spotiflac
```

Common causes:
- `jobs.db` is locked by a previous crashed instance. Remove the lock file: `rm /path/to/config/jobs.db.lock` (if it exists — BoltDB doesn't actually create one, but check for a `.lock` or stale process).
- Bad `JELLYFIN_URL` format — must be a valid HTTP URL.

### Old container still running after update

```bash
docker compose down && docker compose pull && docker compose up -d
```

---

## Debug logs

Enable verbose logging via the **Debug Logger** page in the UI (last item in the sidebar). This streams the server's stdout log in real time, useful for diagnosing download failures.
