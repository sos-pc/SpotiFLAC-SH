# Authentication

SpotiFLAC uses **Jellyfin** as its identity provider. All sessions are represented as short-lived JWTs. API keys provide persistent access for external integrations.

---

## Jellyfin Login

### How it works

1. Client sends `POST /api/v1/auth/login` with Jellyfin username + password.
2. SpotiFLAC forwards credentials to the Jellyfin server (`JELLYFIN_URL`).
3. On success, a JWT is returned (24-hour expiry).
4. Every subsequent request carries the token in `Authorization: Bearer <token>`.

### Admin vs. regular users

- **Admin** in SpotiFLAC = admin in Jellyfin (`IsAdmin` field on the Jellyfin user).
- Admin users can see all download jobs and access the File Manager.
- Regular users only see their own queue, watchlists, and history.

### Rate limiting

`POST /api/v1/auth/login` is rate-limited to **5 failed attempts per 5 minutes** per IP. After that, it returns `429` with a retry-after hint. Successful logins reset the counter.

---

## JWT

Tokens are signed with `HS256` using the secret from the `jwt_secret` file (auto-generated on first run, or set via `JWT_SECRET` env var).

**Payload claims:**

| Claim | Type | Description |
|-------|------|-------------|
| `sub` | string | Jellyfin user ID |
| `name` | string | Display name |
| `is_admin` | bool | Admin flag |
| `exp` | int | Expiry timestamp (24h from issue) |

The token is passed as:
- `Authorization: Bearer <token>` header (recommended)
- `?token=<token>` query parameter (SSE endpoints)

On expiry the server returns `401`. The frontend emits an `auth:expired` event and redirects to the login page.

---

## API Keys

API keys let external tools (scripts, integrations) call SpotiFLAC without going through the Jellyfin login flow. They never expire unless revoked.

### Creating a key

Via the UI: **Settings → API Keys → Create key**

Via API:
```bash
curl -s -X POST http://spotiflac.example.com/api/v1/auth/keys \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-script","permissions":["download"]}'
```

Response (key shown **once only** — copy it immediately):
```json
{ "id": "abc123", "name": "my-script", "key": "sk_spotiflac_e4a9d596..." }
```

### Using a key

```bash
curl -H "X-API-Key: sk_spotiflac_e4a9d596..." \
  http://spotiflac.example.com/api/v1/jobs
```

### Revoking a key

Via the UI: **Settings → API Keys → Revoke**

Via API:
```bash
curl -X DELETE http://spotiflac.example.com/api/v1/auth/keys/abc123 \
  -H "Authorization: Bearer <jwt>"
```

---

## LAN Bypass (`DISABLE_AUTH_ON_LAN`)

When `DISABLE_AUTH_ON_LAN=true`, requests arriving **directly** from a local IP address are automatically authenticated as a local admin — no Jellyfin login required.

### Trusted IP ranges

| Range | Example |
|-------|---------|
| Loopback | `127.0.0.1`, `::1` |
| Private A | `10.0.0.0/8` |
| Private B | `172.16.0.0/12` (includes Docker bridge) |
| Private C | `192.168.0.0/16` |

### How the check works

SpotiFLAC trusts **only `RemoteAddr`** — not `X-Forwarded-For` or `X-Real-IP`. This means:

- Direct LAN request → LAN IP in `RemoteAddr` → auto-login ✅
- Request via Nginx/SWAG → `RemoteAddr` is `127.0.0.1` but `X-Forwarded-For` is present → normal Jellyfin login enforced ✅
- Port 6890 exposed publicly + feature enabled → **security risk** ⚠️

### Verification before enabling

```bash
# Run from an external machine — should time out
curl -m 5 -X POST http://$(curl -s ifconfig.me):6890/api/v1/auth/local
# If it responds, do NOT enable DISABLE_AUTH_ON_LAN
```

---

## Tidal Account (Optional)

See [tidal-auth.md](tidal-auth.md) for the full PKCE flow. This is separate from SpotiFLAC authentication — it authorizes SpotiFLAC to use your personal Tidal Premium subscription for higher-reliability downloads. It is entirely optional; without it, SpotiFLAC uses community HiFi proxies.
