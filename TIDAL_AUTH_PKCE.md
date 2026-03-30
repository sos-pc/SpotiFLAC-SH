# Tidal Authentication

SpotiFLAC uses the **OAuth 2.0 Device Code flow** to authenticate personal Tidal accounts.

See [`docs/tidal-auth.md`](docs/tidal-auth.md) for the complete guide.

## Quick reference

### UI (recommended)

Settings → **Tidal Account** → Connect with Tidal → open the authorization link → SpotiFLAC detects confirmation automatically.

### curl

```bash
# 1. Start
curl -s -X POST http://<HOST>:6890/api/v1/auth/tidal/device/start \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" -d '{}'

# 2. Open verification_uri_complete in browser, authorize

# 3. Poll (repeat every 5s until status=authorized)
curl -s -X POST http://<HOST>:6890/api/v1/auth/tidal/device/poll \
  -H "Authorization: Bearer <TOKEN>" -H "Content-Type: application/json" \
  -d '{"device_code":"<device_code from step 1>"}'

# Status
curl -s http://<HOST>:6890/api/v1/auth/tidal/status -H "Authorization: Bearer <TOKEN>"

# Disconnect
curl -s -X DELETE http://<HOST>:6890/api/v1/auth/tidal -H "Authorization: Bearer <TOKEN>"
```
