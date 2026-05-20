# Tidal Authentication

SpotiFLAC uses the **OAuth 2.0 Device Code Flow** to attach a personal Tidal Premium account.

> The full guide lives at [`docs/tidal-auth.md`](docs/tidal-auth.md). This file is a quick reference for the API surface.

## Quick reference

### UI (recommended)

Settings → **Tidal Account** → Connect with Tidal → open the authorization link → SpotiFLAC detects the confirmation automatically.

### Helper script

```bash
python3 auth_tidal.py --host http://<HOST>:6890 --token <YOUR_JWT_OR_API_KEY>
```

### curl

```bash
# 1. Start
curl -s -X POST http://<HOST>:6890/api/v1/auth/tidal/device/start \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" -d '{}'
# Returns: device_code, user_code, verification_uri_complete, expires_in, interval

# 2. Open verification_uri_complete in browser, log in with Tidal Premium, confirm

# 3. Poll every `interval` seconds until status=authorized
curl -s -X POST http://<HOST>:6890/api/v1/auth/tidal/device/poll \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"device_code":"<from step 1>"}'

# Status
curl -s http://<HOST>:6890/api/v1/auth/tidal/status \
  -H "Authorization: Bearer <TOKEN>"

# Disconnect
curl -X DELETE http://<HOST>:6890/api/v1/auth/tidal \
  -H "Authorization: Bearer <TOKEN>"
```

> Previously this file was named `TIDAL_AUTH_PKCE.md` because SpotiFLAC used a PKCE Web OIDC flow. The PKCE flow has been retired — the project now uses Device Code exclusively. If you have an old bookmark, update it to this URL.
