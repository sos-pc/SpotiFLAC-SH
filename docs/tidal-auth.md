# Tidal Authentication

SpotiFLAC works without any Tidal account — it falls back to community HiFi proxies automatically. Authenticating with a **personal Tidal Premium account** gives better reliability and access to Hi-Res FLAC.

> **Requires an active, paid Tidal subscription.** Free accounts do not receive the `playback` scope.

---

## How it works

SpotiFLAC uses the **OAuth 2.0 Device Code flow**. No redirect URL to copy, no browser callback — you open a Tidal authorization page, confirm, and SpotiFLAC detects the authorization automatically.

---

## Option A — UI (recommended)

1. Open SpotiFLAC → **Settings → Tidal Account**.
2. Click **Connect with Tidal**.
3. Click **Open Tidal authorization page** — the Tidal login page opens in a new tab.
4. Log in with your Tidal Premium account and confirm the authorization.
5. SpotiFLAC detects the confirmation automatically (polling every 5 seconds). Status changes to **Connected**.

No copy-paste required.

---

## Option B — Manual (curl)

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

### Step 3 — Poll until authorized

```bash
curl -s -X POST http://your-spotiflac-host:6890/api/v1/auth/tidal/device/poll \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"device_code":"abc123..."}'
```

Repeat every 5 seconds. Possible responses:

| `status` | Meaning |
|---|---|
| `pending` | User hasn't authorized yet — keep polling |
| `authorized` | Token saved, connection established |
| `expired` | The 5-minute window passed — start again |
| `denied` | User refused — start again |
| `error` | Unexpected error — see `error` field |

---

## Checking status

```bash
curl -s http://your-spotiflac-host:6890/api/v1/auth/tidal/status \
  -H "Authorization: Bearer <your-jwt>"
```

```json
{ "connected": true, "expires_at": 1753920000 }
```

---

## Token lifecycle

- Tokens are stored in `tidal_token.json` in the config directory.
- SpotiFLAC **automatically refreshes** the token before it expires — no re-authentication needed.
- If the refresh fails (e.g. subscription lapsed), SpotiFLAC falls back to community proxies transparently.

---

## Disconnecting

Via UI: **Settings → Tidal Account → Disconnect**

Via API:
```bash
curl -s -X DELETE http://your-spotiflac-host:6890/api/v1/auth/tidal \
  -H "Authorization: Bearer <your-jwt>"
```

---

## Fallback behaviour

If no Tidal token is present (or it has expired), SpotiFLAC seamlessly routes downloads through the community proxy pool:

```
Tidal personal token present → use official Tidal API
        ↓ (absent / expired)
Community HiFi proxies  → triton.squid.wtf, api.monochrome.tf, wolf.qqdl.site, …
        ↓ (all proxies fail)
Qobuz community proxies → dab.yeet.su, dabmusic.xyz, qbz.afkarxyz.fun
        ↓ (fail)
Amazon Music proxy
        ↓ (fail)
Deezer proxy
        ↓ (fail)
Job marked as failed (retried on next sync or manual retry)
```

The proxy list is configurable in **Settings → APIs → Proxy Configuration** without restarting the server.
