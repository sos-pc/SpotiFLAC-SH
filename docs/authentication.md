# Authentication

SpotiFLAC uses **Jellyfin** as its identity provider. All sessions are represented as short-lived JWTs. API keys provide persistent access for external integrations.

The whole flow is implemented in two files:

- `auth.go` — Jellyfin authentication, JWT issuance and validation, the `RequireAuth` and `localBypassMiddleware` helpers.
- `api_keys.go` — API key creation, hashing (`SHA-256`), validation.

---

## Jellyfin Login

### How it works

1. Client sends `POST /api/v1/auth/login` with Jellyfin username + password.
2. SpotiFLAC forwards the credentials to the Jellyfin server (`JELLYFIN_URL`) using the `MediaBrowser Client="SpotiFLAC", DeviceId="spotiflac"` `X-Emby-Authorization` header.
3. On `200`, SpotiFLAC upserts the user in BoltDB (`users` bucket) and issues a JWT (24-hour expiry).
4. Every subsequent request carries the token in `Authorization: Bearer <token>` (or `?token=<jwt>` for SSE endpoints).

### Admin vs. regular users

- **Admin** in SpotiFLAC = `IsAdministrator: true` in the Jellyfin user policy. The flag is refreshed on every successful login (so demoting a user in Jellyfin will demote them in SpotiFLAC after their next sign-in).
- Admin users can see all download jobs in the queue and access the File Manager.
- Regular users only see their own queue, watchlists and history (filtered server-side by `userID`).

### Rate limiting

Implemented in `ratelimit.go` (`LoginRateLimiter`). Per source IP:

| Constant | Value |
|----------|-------|
| Window | 1 minute |
| Max attempts per window | 10 |
| Block duration on overflow | 5 minutes |
| Cleanup tick | 10 minutes |

When the limit is exceeded, `POST /api/v1/auth/login` returns `429` with `Retry-After: 300`. **Successful logins do *not* reset the counter** — the only way out of a block is to wait 5 minutes.

The IP comparison is done against `r.RemoteAddr`, falling back to the leftmost `X-Forwarded-For` value **only** when the immediate connection comes from a private/loopback address (i.e. behind a trusted reverse proxy). Always make sure your reverse proxy forwards `X-Forwarded-For`, otherwise every internet user shares one bucket.

---

## JWT

Tokens are signed with `HMAC-SHA256` using the secret from the `jwt_secret` file (auto-generated on first run with `crypto/rand`, mode `0600`) or from the `JWT_SECRET` env var if set. Env var takes precedence.

**Header:** `{"alg":"HS256","typ":"JWT"}` (no extra claims).

**Payload claims:**

| Claim | Type | Description |
|-------|------|-------------|
| `uid` | string | Jellyfin user ID (or `local-admin` for LAN bypass) |
| `name` | string | Display name |
| `admin` | bool | Admin flag |
| `exp` | int | Expiry timestamp, Unix seconds (24 h from issue) |

> The claim names follow the abbreviated form used in `auth.go` (`UserID` → `uid`, `DisplayName` → `name`, `IsAdmin` → `admin`, `ExpiresAt` → `exp`). They are **not** the standard `sub`/`iat` claims — keep this in mind if you decode them with a generic JWT library.

The token is passed as:
- `Authorization: Bearer <token>` header (recommended)
- `?token=<token>` query parameter (used by SSE endpoints — `EventSource` cannot set custom headers)

On expiry, every protected handler returns `401`. The frontend listens and dispatches an `auth:expired` event, redirecting to the login page.

### Verifying a token manually

```bash
TOKEN=<your-jwt>
curl -s http://spotiflac.example.com/api/v1/auth/me \
  -H "Authorization: Bearer $TOKEN" | jq
# { "id": "...", "display_name": "Alice", "is_admin": true }
```

---

## API Keys

API keys let external tools (scripts, Home Assistant, monitoring) call SpotiFLAC without going through Jellyfin login. They are persistent and never expire unless revoked.

### Storage model

The raw key is **never persisted**. On creation:

1. SpotiFLAC generates 24 bytes of cryptographic randomness, hex-encoded, prefixed with `sk_spotiflac_` → 60-character key.
2. The SHA-256 of the raw key is stored in BoltDB (`apikeys` bucket).
3. The raw key is returned to the client **once** in the response — there is no way to recover it later.

Validation is a linear scan of the bucket on every request that uses `X-API-Key`; the cost is negligible for `n < ~50` keys.

### Permissions

Permission strings recognized by the system:

| Permission | Effect |
|------------|--------|
| `read` | (Reserved — no enforcement yet) |
| `download` | (Reserved — no enforcement yet) |
| `admin` | Sets `IsAdmin: true` on the synthesized JWT claims, granting access to admin-only routes |

If you don't pass `permissions` when creating a key, the default is `["read", "download"]`. Only `admin` is currently enforced server-side; the others document the *intended* scope for the human reading the key list.

### Creating a key

Via the UI: **Settings → API Keys → Create key**. The raw key is shown in a copy-friendly modal — copy it immediately.

Via API:
```bash
curl -s -X POST http://spotiflac.example.com/api/v1/auth/keys \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-script","permissions":["read","download"]}'
```

Response (`201`):
```json
{
  "key": "sk_spotiflac_e4a9d596...",
  "id": "key-...",
  "name": "my-script",
  "permissions": ["read", "download"],
  "created_at": "2026-05-19T00:00:00Z"
}
```

### Using a key

```bash
curl -H "X-API-Key: sk_spotiflac_e4a9d596..." \
  http://spotiflac.example.com/api/v1/jobs
```

When validated, the key is mapped to a synthetic `JWTClaims` value:

- `UserID = key.UserID`
- `DisplayName = "API Key: <name>"`
- `IsAdmin = (key.Permissions contains "admin")`
- `ExpiresAt = 0` (no expiry)

The `LastUsedAt` timestamp on the key is updated asynchronously (`go a.touchAPIKey(...)`) — no blocking on the request hot path.

### Revoking a key

Via the UI: **Settings → API Keys → Revoke**.

Via API:
```bash
curl -X DELETE http://spotiflac.example.com/api/v1/auth/keys/<id> \
  -H "Authorization: Bearer <jwt>"
```

Returns `204`. Returns `400 access denied` if the key does not belong to the calling user.

---

## LAN Bypass (`DISABLE_AUTH_ON_LAN`)

When `DISABLE_AUTH_ON_LAN=true`, requests arriving **directly** from a local IP are automatically authenticated as a local admin — no Jellyfin login required.

### Trusted IP ranges

Implemented in `server.go` (`isLocalIP`):

| Range | Notes |
|-------|-------|
| `127.0.0.0/8` | Loopback (incl. SSH tunnels back to localhost) |
| `::1/128` | IPv6 loopback |
| `10.0.0.0/8` | RFC-1918 private |
| `172.16.0.0/12` | Includes the default Docker bridge (`172.17.0.0/16`) |
| `192.168.0.0/16` | RFC-1918 private |

### How the check works

SpotiFLAC trusts **only `r.RemoteAddr`** — not `X-Forwarded-For` or `X-Real-IP`. The presence of either of those headers automatically disqualifies the bypass:

| Scenario | Result |
|----------|--------|
| Direct LAN request | `RemoteAddr` is private, no XFF → auto-login as `local-admin` |
| Request via Nginx/SWAG/Caddy | `XFF` is set → bypass refused, normal Jellyfin login enforced |
| Public exposure on port 6890 | If `RemoteAddr` is private (rare on public IPs) and no proxy headers → bypass would trigger. **Keep port 6890 closed publicly.** |

The bypass is implemented in two places:

- `localBypassMiddleware` injects a synthetic admin JWT in the `Authorization` header so the per-route auth still works.
- `POST /api/v1/auth/local` lets clients explicitly request a token; returns `403 local bypass not enabled` when the conditions aren't met.

### Verification before enabling

```bash
# Run from an external machine — should time out
curl -m 5 -X POST http://$(curl -s ifconfig.me):6890/api/v1/auth/local
# If it responds with a token, do NOT enable DISABLE_AUTH_ON_LAN
```

---

## Tidal Account (Optional)

See [tidal-auth.md](tidal-auth.md) for the full Device Code Flow walkthrough. This authorizes SpotiFLAC to use your personal Tidal Premium subscription for full FLAC downloads. **This is separate from SpotiFLAC authentication** — it is a per-server (not per-user) credential, stored in `<config>/tidal_token.json`. Without it, SpotiFLAC falls back to community HiFi proxies (which currently return preview-only audio).
