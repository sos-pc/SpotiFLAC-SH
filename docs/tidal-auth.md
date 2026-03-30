# Tidal Authentication (PKCE)

SpotiFLAC works without any Tidal account — it falls back to community HiFi proxies automatically. Authenticating with a **personal Tidal Premium account** gives better reliability and access to Hi-Res FLAC.

> **Requires an active, paid Tidal subscription.** Free accounts do not receive the `playback` scope.

---

## Background

Tidal's legacy Device Flow (TV/Android Auto client IDs) no longer grants the `playback` scope as of March 2026. SpotiFLAC uses the **PKCE Web OIDC flow**, which mimics the official Tidal Web Player (`listen.tidal.com`) and is not subject to this restriction.

---

## Option A — UI (recommended)

1. Open SpotiFLAC → **Settings → Tidal Account**.
2. Click **Connect with Tidal** — a new browser tab opens with the Tidal login page.
3. Log in with your Tidal Premium account.
4. After login, copy the full URL from your browser's address bar (it looks like `https://listen.tidal.com/login/auth?code=...`).
5. Paste it into the **Callback URL** field in Settings and click **Submit**.
6. Status changes to **Connected**.

---

## Option B — Automated script

```bash
python3 auth_tidal.py --host http://your-spotiflac-host:6890
```

The script opens the Tidal login page, prompts you to paste the callback URL, and handles all the API exchanges.

---

## Option C — Manual (curl)

### Step 1 — Get the login URL

```bash
curl -s http://your-spotiflac-host:6890/api/v1/auth/tidal/url \
  -H "Authorization: Bearer <your-jwt>"
```

Response:
```json
{ "url": "https://login.tidal.com/authorize?appMode=web&client_id=txNoH4kkV41MfH25&code_challenge=...&response_type=code..." }
```

### Step 2 — Log in

Open the `url` in a browser. Log in with your Tidal Premium account.

### Step 3 — Copy the callback URL

After login, Tidal redirects your browser. Copy the full URL from the address bar:

```
https://listen.tidal.com/login/auth?code=abc123def456&lang=en
```

### Step 4 — Exchange the code

```bash
curl -s -X POST http://your-spotiflac-host:6890/api/v1/auth/tidal/callback \
  -H "Authorization: Bearer <your-jwt>" \
  -H "Content-Type: application/json" \
  -d '{"callback_url":"https://listen.tidal.com/login/auth?code=abc123def456&lang=en"}'
```

Returns `204` on success.

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

- Tokens are cached in `tidal_token.json` in the config directory.
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

If no Tidal token is present (or it has expired), SpotiFLAC seamlessly routes downloads through the community HiFi proxy pool:

```
Tidal PKCE token present → use official Tidal API
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
