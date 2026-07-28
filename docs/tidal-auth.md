# Tidal Authentication

SpotiFLAC works without any Tidal account — it falls back to community HiFi proxies automatically. Authenticating with a **personal Tidal Premium account** unlocks full FLAC (`HI_RES_LOSSLESS`); without authentication, community proxies currently serve preview-only audio.

> **Requires an active, paid Tidal subscription.** Free accounts do not receive the `playback` scope.

---

## How it works

SpotiFLAC uses the **OAuth 2.0 Device Code flow** (RFC 8628). No redirect URL to copy, no browser callback — you open the displayed authorization page, confirm, and SpotiFLAC detects the authorization automatically by polling Tidal's token endpoint.

The flow uses the public application credentials shipped by `orpheusdl-tidal` and other community Tidal projects (see [CREDITS.md](../CREDITS.md)). They are not tied to any user account.

The token is stored in `<config>/tidal_token.json` (mode `0644`):

```json
{
  "access_token":  "...",
  "refresh_token": "...",
  "expires_in":    604800,
  "expires_at":    1753920000,
  "client_id":     "4N3n6Q1x95LL5K7p",
  "country_code":  "FR"
}
```

The refresh path runs in `GetValidTidalToken`: 5 minutes before expiry, the refresh endpoint is hit. On failure, the file is deleted and SpotiFLAC falls back to community proxies.

---

## Option A — UI (recommended)

1. Open SpotiFLAC → **Settings → Tidal Account**.
2. Click **Connect with Tidal**.
3. Click **Open Tidal authorization page** — Tidal opens in a new tab.
4. Log in with your Tidal Premium account and confirm the authorization.
5. SpotiFLAC detects the confirmation automatically (polling every 5 seconds). Status changes to **Connected**.

No copy-paste required.

---

## Option B — Helper script

```bash
python3 auth_tidal.py --host http://your-spotiflac-host:6890 --token <your-jwt-or-api-key>
```

The script wraps the manual flow below: starts the device auth, opens the verification URL in your browser, and polls until completion. Compatible with both JWT (`Authorization: Bearer ...`) and API keys (`X-API-Key: sk_spotiflac_...`) — auto-detected from the `--token` value.

---

## Option C — Manual (curl)

### Step 1 — Start the device auth flow

```bash
curl -s -X POST http://your-spotiflac-host:6890/api/v1/auth/tidal/device/start \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{}'
```

Response:
```json
{
  "device_code": "abc123...",
  "user_code": "LDANN",
  "verification_uri": "https://link.tidal.com/LDANN",
  "verification_uri_complete": "https://link.tidal.com/LDANN",
  "expires_in": 300,
  "interval": 5
}
```

### Step 2 — Open the authorization URL

Open `verification_uri_complete` in a browser. Log in with your Tidal Premium account and confirm.

> Some browsers block the `link.tidal.com` redirect when not authenticated. If the page errors, log in to `tidal.com` first, then revisit the URL.

### Step 3 — Poll until authorized

```bash
curl -s -X POST http://your-spotiflac-host:6890/api/v1/auth/tidal/device/poll \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"device_code":"abc123..."}'
```

Repeat every `interval` seconds (5 s by default). Possible responses:

| `status` | Meaning |
|---|---|
| `pending` | User hasn't authorized yet — keep polling |
| `authorized` | Token saved, connection established |
| `expired` | The 5-minute window passed — start again from Step 1 |
| `denied` | User refused the authorization — start again if intended |
| `error` | Unexpected error — see `error` field |

---

## Checking status

```bash
curl -s http://your-spotiflac-host:6890/api/v1/auth/tidal/status \
  -H "Authorization: Bearer <your-jwt>"
```

```json
{ "connected": true,  "expires_at": 1753920000 }
{ "connected": false }
```

`expires_at` is Unix seconds. If it's in the past, the token will be refreshed transparently on the next download — no action required.

---

## Token lifecycle

- Stored in `<config>/tidal_token.json`.
- **Auto-refreshed** 5 minutes before expiry by `GetValidTidalToken`.
- On refresh failure (revoked, subscription lapsed, network outage longer than the expiry window), the file is deleted via `DeleteTidalToken` and SpotiFLAC falls back to community proxies transparently.
- The country code (`country_code`) is fetched once via `GET /v1/sessions` and cached on the token; it's used for region-specific track availability.

---

## Disconnecting

Via UI: **Settings → Tidal Account → Disconnect**.

Via API:
```bash
curl -s -X DELETE http://your-spotiflac-host:6890/api/v1/auth/tidal \
  -H "Authorization: Bearer <your-jwt>"
```

Returns `204`. Removes the in-memory cache and deletes `tidal_token.json`.

---

## Fallback chain

When SpotiFLAC needs a Tidal stream URL:

```
Personal token present and valid
        ↓
api.tidal.com (official, full FLAC)
        ↓ (no token / refresh failed / 4xx)
Community HiFi proxies — tried in configured order (util.GetTidalProxies)
        ↓ (every proxy fails)
Provider chain continues to Qobuz / Amazon / Deezer per `autoOrder`
        ↓ (every provider fails)
Job marked `failed` (retried on next manual sync via SyncWatchlist)
```

> **As of May 2026**, every community proxy responds with `assetPresentation: "PREVIEW"` (30-second preview only) without a personal token. Authenticating is the only way to get full FLAC from Tidal.

The proxy list is configurable in **Settings → APIs → Proxy Configuration** without restarting the server. Auto-discovered proxies are listed read-only — copy them into the user list to make them persistent.

---

## Why "Device Code" and not PKCE?

A previous version of SpotiFLAC used the PKCE Web OIDC flow (`/login/auth?code=...` callback). It was replaced by Device Code because:

- No callback URL to configure or manage.
- Works identically in headless deployments and behind reverse proxies.
- Same flow used by `tiddl`, `orpheusdl-tidal`, and other community projects, so credentials are well-known and proven.

The endpoints and code paths for the older PKCE flow have been removed. Some leftover frontend RPC stubs (`GetTidalAuthURL`, `SubmitTidalCallback`) still exist but are unused — they will be removed in a future refactor.
