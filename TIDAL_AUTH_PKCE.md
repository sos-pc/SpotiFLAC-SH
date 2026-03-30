# Tidal Authentication (PKCE Web Flow)

Due to changes in Tidal's OAuth API (March 2026), the traditional "Device Flow" no longer grants the `playback` scope, preventing direct FLAC downloads even with a Premium account.

SpotiFLAC implements the **PKCE (Proof Key for Code Exchange) Web Flow**, mimicking the official Tidal Web Player (`listen.tidal.com`).

> **⚠️ IMPORTANT:** You **MUST** have an active, paid Tidal subscription. Free accounts will not be granted the `playback` scope. Without a token, SpotiFLAC automatically falls back to community-hosted HiFi APIs.

See [`docs/tidal-auth.md`](docs/tidal-auth.md) for the complete guide including UI instructions, token lifecycle, and the fallback waterfall.

---

## Quick reference

### Option A — UI (easiest)

Settings → **Tidal Account** → Connect with Tidal → paste callback URL → Submit.

### Option B — Automated script

```bash
python3 auth_tidal.py --host http://<YOUR-SPOTIFLAC-IP>:6890
```

### Option C — Manual (curl)

#### Step 1 — Get the login URL
```bash
curl -s http://<YOUR-SPOTIFLAC-IP>:6890/api/v1/auth/tidal/url \
  -H "Authorization: Bearer <YOUR-JWT>"
```

Response:
```json
{ "url": "https://login.tidal.com/authorize?appMode=web&client_id=txNoH4kkV41MfH25&code_challenge=...&response_type=code..." }
```

#### Step 2 — Log in via browser
Open the URL, log in with your Premium Tidal account.

#### Step 3 — Copy the redirect URL
After login, copy the full URL from the browser's address bar:
```
https://listen.tidal.com/login/auth?code=abc123def456&lang=en
```

#### Step 4 — Exchange the code
```bash
curl -s -X POST http://<YOUR-SPOTIFLAC-IP>:6890/api/v1/auth/tidal/callback \
  -H "Authorization: Bearer <YOUR-JWT>" \
  -H "Content-Type: application/json" \
  -d '{"callback_url":"https://listen.tidal.com/login/auth?code=abc123def456&lang=en"}'
```

Returns `204` on success. The token is saved to `tidal_token.json` and auto-refreshed.

---

## Check status

```bash
curl -s http://<YOUR-SPOTIFLAC-IP>:6890/api/v1/auth/tidal/status \
  -H "Authorization: Bearer <YOUR-JWT>"
```

```json
{ "connected": true, "expires_at": 1753920000 }
```

## Disconnect

```bash
curl -s -X DELETE http://<YOUR-SPOTIFLAC-IP>:6890/api/v1/auth/tidal \
  -H "Authorization: Bearer <YOUR-JWT>"
```
