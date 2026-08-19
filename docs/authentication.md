# Authentication

SpotiFLAC uses **Jellyfin** as its identity provider. All sessions are represented as short-lived JWTs. API keys provide persistent access for external integrations.

The whole flow is implemented in three files:

- `auth.go` — Jellyfin authentication, JWT issuance and validation, and the `RequireAuth` helper.
- `api_keys.go` — API key creation, hashing (`SHA-256`), validation.
- `ratelimit.go` — login rate limiter.

---

## Jellyfin Login

### How it works

1. Client sends `POST /api/v1/auth/login` with Jellyfin username + password.
2. SpotiFLAC forwards the credentials to the Jellyfin server (`JELLYFIN_URL`) using the `MediaBrowser Client="SpotiFLAC", DeviceId="spotiflac"` `X-Emby-Authorization` header.
3. On `200`, SpotiFLAC upserts the user in BoltDB (`users` bucket) and issues a JWT (24-hour expiry).
4. Every subsequent request carries the token in `Authorization: Bearer <token>` — except SSE connections and the browser job-download link, which can't set custom headers and use a short-lived **stream token** instead (see "Stream tokens" below), not the raw session JWT.

### Admin vs. regular users

- **Admin** in SpotiFLAC = `IsAdministrator: true` in the Jellyfin user policy. The flag is refreshed on every successful login (so demoting a user in Jellyfin will demote them in SpotiFLAC after their next sign-in).
- Admin users can see all download jobs in the queue, access the File Manager, and call admin-only endpoints (notably `POST /api/v1/admin/retag-legacy`).
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
| `uid` | string | Jellyfin user ID |
| `name` | string | Display name |
| `admin` | bool | Admin flag |
| `exp` | int | Expiry timestamp, Unix seconds |
| `tv` | int | Token version — must match the user's current `UserProfile.TokenVersion` at validation time, or the token is rejected even if `exp` hasn't passed yet |
| `perms` | string[] | Present only on API-key-derived claims (omitted for a normal session) |
| `is_key` | bool | `true` only for API-key-derived claims |
| `scope` | string | Empty for a normal session; `"stream"` for a stream token (see below) — a stream-scoped token is rejected on every endpoint outside its narrow allow-list |

> The claim names follow the abbreviated form used in `auth.go` (`UserID` → `uid`, `DisplayName` → `name`, `IsAdmin` → `admin`, `ExpiresAt` → `exp`, `TokenVersion` → `tv`, `Permissions` → `perms`, `IsAPIKey` → `is_key`, `Scope` → `scope`). They are **not** the standard `sub`/`iat` claims — keep this in mind if you decode them with a generic JWT library.

**Token revocation (`tv`).** JWTs are otherwise fully stateless — there's no server-side blocklist, so a normal session JWT stays valid for its full 24h lifetime no matter what happens to the account afterward. `tv` is the one exception: when a user's Jellyfin admin flag changes, `UserProfile.TokenVersion` is bumped on their next login, which immediately invalidates every JWT issued before that bump (they still decode fine, but `tv` no longer matches and the request is rejected) — without this, a demoted admin's existing token would keep working with the old privilege level for up to 24h.

**Stream tokens.** `Authorization: Bearer <token>` is the normal path (recommended for everything except SSE/download links). SSE connections (`EventSource`) and the browser's "Download" `<a href>` link can't set custom headers, so those instead call `GET /api/v1/auth/stream-token` first to mint a **60-second**, `scope: "stream"` token and put *that* in the URL (`?token=<stream-token>`) — never the 24h session JWT. A stream token is rejected on every endpoint except the small allow-list it was minted for (`streamScopedPaths` / `isJobDownloadPath` in `api_v1.go`), so even if it leaks into a reverse-proxy access log or browser history, it's both short-lived and narrowly scoped.

On expiry (or `tv` mismatch), every protected handler returns `401`. The frontend listens and dispatches an `auth:expired` event, redirecting to the login page.

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
| `read` | Grants read-only routes (`v1RequirePermission(..., "read")`) — search, metadata, job/history listings, file browsing, etc. |
| `manage` | Grants mutating routes (`v1RequirePermission(..., "manage")`) — triggering downloads, settings, watchlist management. Renamed from `download`; keys created before the rename still carry `"download"` and are still honored (`v1RequirePermission` treats `"download"` as an alias for `"manage"`). |
| `admin` | Sets `IsAdmin: true` on the synthesized JWT claims, granting access to admin-only routes (e.g. `POST /api/v1/admin/retag-legacy`) |

**Only API keys are checked against this list at all** — `v1RequirePermission` short-circuits to "allowed" for every normal Jellyfin-session JWT (admin or not); the permission model exists specifically to let an API key be scoped *narrower* than the account that created it, not to restrict logged-in users.

If you don't pass `permissions` when creating a key, the default is `["read", "manage"]`. A non-admin account can never self-issue a key with `admin` permission — even though a non-admin's own session can otherwise call `POST /api/v1/auth/keys` freely, the handler rejects `"admin"` in the requested list unless the caller is already an admin (a key would otherwise be a way to mint a permanent, non-expiring admin credential from a demoted or never-admin account).

### Creating a key

Via the UI: **Settings → API Keys → Create key**. The raw key is shown in a copy-friendly modal — copy it immediately.

Via API:
```bash
curl -s -X POST http://spotiflac.example.com/api/v1/auth/keys \
  -H "Authorization: Bearer <jwt>" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-script","permissions":["read","manage"]}'
```

Response (`201`):
```json
{
  "key": "sk_spotiflac_e4a9d596...",
  "id": "key-...",
  "name": "my-script",
  "permissions": ["read", "manage"],
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
- `IsAdmin = (key.Permissions contains "admin")` — re-checked live against the owning account's **current** admin status, not just baked in at key-creation time, so a key created while its account was admin loses admin API access the moment that account is demoted (its other permissions still work)
- `ExpiresAt = 0` (no expiry)
- `Permissions = key.Permissions` — this is what `v1RequirePermission` checks on every route
- `IsAPIKey = true` — this is what makes `v1RequirePermission` check `Permissions` at all; a normal session JWT never has this set and always passes

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


## Tidal Account (Optional)

See [tidal-auth.md](tidal-auth.md) for the full Device Code Flow walkthrough. This authorizes SpotiFLAC to use your personal Tidal Premium subscription for full FLAC downloads. **This is separate from SpotiFLAC authentication** — it is a per-server (not per-user) credential, stored in `<config>/tidal_token.json`. Without it, SpotiFLAC falls back to community HiFi proxies (which currently return preview-only audio).
