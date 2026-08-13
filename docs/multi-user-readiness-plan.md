# Opening the instance to every Jellyfin user — plan

> **🧭 Plan, 2026-08-13.** The operator intends every Jellyfin account to use
> SpotiFLAC, not just the admin. This audits what that changes. It was triggered
> by a feature request — connect a Spotify account to pick playlists from a list
> — which is deferred to §6 because the features it would expose are not ready
> to be shared. Companions: [authentication.md](authentication.md) ·
> [settings-reference.md](settings-reference.md) ·
> [watchlist-consistency-plan.md](watchlist-consistency-plan.md).

## 0. The finding

Multi-user is not missing. It is half-built, and the half that exists is good:
jobs carry a `UserID`, the SSE stream filters on it (`sse.go:192`), history and
exports take `(userID, isAdmin)`, watchlist M3U8 names already disambiguate two
accounts watching the same personalised playlist
(`internal/watcher/watcher.go:2206`), the catalog records `downloaded_by`, and
every destructive or instance-wide route is guarded by `v1RequireAdmin` with a
test that enumerates them (`api_auth_guards_test.go`).

What is missing is not isolation. **It is a boundary the operator controls.**
Today a user's own settings define the limits that are supposed to contain them,
and the one shared resource that matters — the download worker — is handed out
first-come-first-served. Both are invisible while the only user is the admin.

The model this instance wants is not tenant isolation. It is a household: one
library, one Tidal account, one engine, several people. The work below is about
sharing that household fairly and safely, not about walling people off.

## 1. A user's own setting is their own confinement root

**Severity: highest. Fix before onboarding anyone.**

`libraryRootForUser` (`api_v1.go:95`) resolves the root that confines a request
by reading that same user's `downloadPath` override:

```go
func (s *Server) libraryRootForUser(userID string) string {
	if root := settings.EffectiveDownloadSettings(s.ctr.Auth, userID).DownloadPath; root != "" {
		return filepath.Clean(root)
	}
	return filepath.Clean(util.GetDefaultMusicPath())
}
```

`cleanLibraryPath` (`api_v1.go:127`) then correctly refuses any path outside
that root. The confinement logic is sound; the root is not. Nothing validates
that a user's `downloadPath` lies inside an operator-defined boundary —
`SaveUserSettings` (`internal/auth/auth.go:239`) stores the submitted map
verbatim.

So any authenticated user can `PUT /api/v1/settings` with `downloadPath: "/"`
and then read the container filesystem through `GET /api/v1/files` and
`GET /api/v1/files/audio`, both of which require only `read`
(`api_files.go:277`, `api_files.go:294`). That includes `/config`: the Bolt
databases, the JWT signing secret, and the provider credentials.

**Reproduced**, not reasoned about — a non-admin user, their own settings, and
the two functions above, with no HTTP involved:

```
racine de l'operateur          : C:\Users\Administrator\Music
racine appliquee a cet usager  : C:\
=> la racine de l'usager contient celle de l'operateur
ACCEPTE : C:\config\jobs.db     (refuse sous la racine operateur)
ACCEPTE : C:\etc\passwd         (refuse sous la racine operateur)
```

The exploit is not "a user chose a folder". It is that the root meant to
*contain* them ends up containing the operator's own. In the container those
paths are `/config/jobs.db` and friends.

Note what is *not* affected: every mutating file route (delete, move, rename)
is `v1RequireAdmin` (`api_files.go:315` onward). This is a read escalation, not
a write one. It is also not exploitable today, because the only user is the
admin — which is exactly why it has to be fixed before that stops being true.

The probe that produced the output above should ship with the fix, inverted:
asserting that a user's override can no longer widen their root.

**Resolution.** An operator-owned boundary that per-user overrides may only
narrow, never widen:

- Add `libraryRootBoundary` to the instance settings (§2), defaulting to the
  global `downloadPath`.
- `libraryRootForUser` returns the user's override only if `isSubPath(boundary,
  override)`; otherwise it returns the boundary and logs the rejection.
- Reject the write too, at `PUT /api/v1/settings`, so the user is told rather
  than silently confined elsewhere than they asked.

Validating on read *and* on write is deliberate: the write check is the good
error message, the read check is the one that still holds for values already
stored in Bolt before this ships.

## 2. Settings fork on first save, and instance keys fork with them

`GET /api/v1/settings` (`api_files.go:225`) returns the user's own settings if
they have any, else the global ones. `PUT` (`api_files.go:244`) writes the whole
submitted map to the user's profile whenever a user is authenticated — it never
writes the instance settings at all.

Two consequences.

**The operator cannot change a setting for everyone.** The frontend GETs the
merged view and PUTs it back whole, so the first save copies every key into that
user's profile. From then on the user is detached: change `spotFetchAPIUrl`
globally and it reaches nobody who has ever opened the settings page.

**Instance-level keys are user-writable.** Of the keys in
`internal/settings/settings.go:82-100`, these are not per-user by any reading:

| Key | Why it is instance-level |
|---|---|
| `spotFetchAPIUrl` | the engine sidecar; a user repointing it aims the server's requests at a host of their choosing |
| `jellyfinMusicPath` | where M3U8 files are written for one shared Jellyfin |
| `downloadPath` | per-user by design, but only within §1's boundary |

The rest — quality, templates, `autoOrder`, tagging toggles — are legitimately
personal and should stay that way.

Worth saying plainly: making `spotFetchAPIUrl` resolve per user was a
*deliberate* change, and `EffectiveDownloadSettings`' own docstring
(`internal/settings/settings.go:132`) records it as a fix — four call sites were
reading the global file and "silently ignoring" the user's saved value. That was
correct when the only user was the operator. It is not a mistake being corrected
here; it is a decision that does not survive the new context.

**Resolution.** Split the store, not the endpoint:

- Declare which keys are instance-scoped, in one list, in `internal/settings`.
- `PUT /api/v1/settings` splits the submitted map: instance keys are applied
  only for an admin (`v1RequireAdmin`) and rejected with a clear message
  otherwise; personal keys go to the user profile as they do now.
- `GET` returns instance keys from the instance store *always*, overlaid with
  the user's personal keys — so an operator change reaches everyone
  immediately, and the fork stops existing.

This also creates the admin write path that §6 needs for the Spotify client ID,
which currently has nowhere to live.

## 3. One worker, strict FIFO, no fairness

```go
jobWorkers = 1                      // internal/jobs/manager.go:32
queue chan string                   // consumed in arrival order, worker.go:30
```

The codebase already knows what this costs. `internal/watcher/watcher.go:1142`
explains that a large batch can outlast twenty sync cycles because
"`jobWorkers=1` serializes every download across every watchlist through one
shared queue". With one user that is a wait. With several it is one user's
2561-track sync holding the only worker while everyone else's single track sits
behind it.

**Raising `jobWorkers` is not the fix and may not be safe.** It is 1 for reasons
that predate this plan and probably involve the engine sidecar and provider rate
limits; that has to be established before touching it, not assumed.

**Resolution — fairness, not parallelism.** Replace the bare channel with a
selection step that picks the next job round-robin across the users who have
pending work. One user with 2561 queued tracks and three users with one each
then drain as 1-1-1-1, not 2561-1-1-1. The throughput is identical; only the
order changes.

Two details that decide whether this is worth doing:

- The queue currently carries job IDs in a channel, so ordering is fixed at
  submission. Round-robin means selecting from persisted pending jobs instead —
  which is a change to how work is handed out, not a scheduler bolted on top.
- Watchlist syncs and interactive downloads are not the same urgency. A second
  axis — interactive before scheduled — is tempting and should be resisted in
  the first pass: get fairness between people working before fairness between
  kinds of work.

## 4. A full queue strands jobs until the next restart

The buffer is 10000 (`manager.go:282`). `Submit` (`manager.go:335`) offers the
job and, if the channel is full, returns `queued=false` — the job is persisted
and picked up by `recoverPendingJobs` **on the next start**.

The docstring is honest about this and it was a reasonable trade at one user.
Four syncs the size of the reference playlist exceed the buffer. The symptom
would be "my downloads never start", with nothing in the logs saying why,
resolved by a restart nobody would think to perform.

**Resolution.** §3's change removes the channel as the source of ordering, which
removes this failure mode as a side effect: pending work lives in Bolt and is
selected from there. If §3 is deferred, this needs its own fix — a periodic
drain of persisted pending jobs, not a bigger buffer.

## 5. The LAN bypass grants admin, not a guest

`DISABLE_AUTH_ON_LAN=true` makes `localBypassMiddleware` (`server.go:99`)
inject a synthetic **administrator** for any request from a private IP with no
`Authorization` header.

The existing guard is right and worth keeping: the bypass is refused when
`X-Forwarded-For` or `X-Real-IP` is present, so access through the reverse proxy
always authenticates. The exposure is direct LAN access only.

But "on the LAN" is where the household is. With several users, any device on
the network — including one nobody is logged into — is an administrator that
can see every user's jobs and purge them.

**Resolution.** Decide, then encode the decision:

- If the bypass is not needed, unset it and delete the middleware. Simplest, and
  removes a class of problem permanently.
- If it is needed (a wall panel, a script), it must inject a **named,
  non-admin** identity, not an admin — and that identity should be
  operator-configured, so its downloads land somewhere attributable.

*To verify before choosing: whether `DISABLE_AUTH_ON_LAN` is actually set in the
live compose. It is absent from both example files, so it is false by default.*

## 6. Shared by design — to be surfaced, not fixed

These are correct and should stay. They need to be *visible*, because users will
otherwise experience them as bugs.

- **One Tidal account for the whole instance.** `api_auth_guards_test.go` names
  it: "disconnecting or rebinding it affects every user of the instance". It is
  admin-gated. Its concurrent-stream limits are shared by everyone.
- **One library, one catalog.** `downloaded_by` records who triggered each file,
  but the file is everyone's. A user who "already has" a track has it because
  someone else downloaded it.
- **One engine sidecar**, hence §3.

**Resolution.** No code change. One line in the UI where it matters — the
account screen should say the providers are shared, so a user who cannot
download does not conclude their own account is broken.

## 7. Destructive and admin actions now belong to other people

Two that changed meaning the moment a second user exists:

- **"Reset queue"** calls `ClearAllDownloads(userID, isAdmin)`
  (`api_jobs.go:151`). For an admin that wipes *everyone's* jobs, including
  running ones. It gained a two-step confirmation in #67, which said what it
  would do when it only did it to you. It should now name the scope: how many
  jobs, belonging to how many people.
- **The failed-downloads export** (`internal/service/history.go`) emits
  `Track, Artist, Album, Error`. For an admin it aggregates every user's
  failures with no column saying whose. Add an owner column on the admin path.

## 8. Then, and only then: connecting a Spotify account

The feature that started this. Deferred to last on purpose — it brings more
people into a queue that has no fairness (§3) and a settings store that cannot
hold its instance-level client ID (§2).

**Two levels, one identity.** Presenting "public profile" versus "OAuth" as a
choice asks the user a question they cannot answer. It is one thing — your
Spotify identity — at two levels:

- **Declared**: paste a profile URL. No setup, works immediately, public
  playlists only. Stored per user, so it is asked once.
- **Verified**: connect the account. Everything, including private and
  collaborative playlists — and `tracks.total`, which the declared path cannot
  provide.

**Measured, not assumed** (probe run 2026-08-13 against
`spclient.wg.spotify.com/user-profile-view/v3`, with the existing anonymous
web-player token — no OAuth):

- A personal profile returns its playlists exactly: 57 announced, 57 returned,
  55 owned and 2 followed, pagination terminating cleanly at the next offset.
- The corporate `spotify` profile does *not*: 50 requested returned 46, then 38,
  then 36, and the announced total drifted 1545 → 1537 → 1535 → 1646 across four
  calls. **Paginate to exhaustion; never derive a page count from the total;
  never treat a short page as the last.**
- An entry carries `uri, name, image_url, followers_count, owner_name,
  owner_uri` — and no track count. `owner_uri` is what separates owned from
  followed.

**Structural decision to make on day one.** Model the playlist source as an
interface returning a normalised entry with an *optional* track count. The
declared path fills it without; OAuth fills it with. Skipping this means
rewriting the picker when OAuth lands.

**OAuth prerequisites, in order.** An HTTPS redirect URI is mandatory —
loopback does not help, since the redirect runs in the user's browser, not on
the server. Each deployment must register its own Spotify application; a shared
client ID would cap the entire project at Spotify's 25-user development-mode
limit and hang it on one person's developer account. Use Authorization Code +
PKCE so no client secret is stored; only refresh tokens are, per user.

The one UI detail that decides whether this works for anyone but its author: the
redirect URI must be displayed **pre-filled from the origin the admin is
actually on**, with a copy button, and must refuse to proceed with a clear
message when that origin is not HTTPS.

Out of scope for a first version: Liked Songs. They are not a playlist, have no
URL, and the entire watcher is keyed on `SpotifyURL`.

## 9. Sequence

1. **§1 and §2 together.** They are one change: an instance/user settings
   boundary. §1 is why it is urgent; §2 is the mechanism, and it creates the
   admin write path everything later needs.
2. **§3, with §4 falling out of it.** Establish first *why* `jobWorkers` is 1 —
   that answer decides whether fairness is enough or throughput must also be
   addressed.
3. **§5 and §7.** Small, independent, and §5 may be a deletion.
4. **§8.** The playlist picker, bulk add, and the declared path. OAuth after,
   as a second source behind the same interface.

## 10. Explicitly not in this plan

- Per-user quotas or storage limits. Fairness (§3) addresses the actual
  complaint — waiting behind someone else — without inventing policy the
  operator has not asked for.
- Splitting the library per user. The shared library is the point of a Jellyfin
  household.
- Rate-limiting the API. No evidence of a problem, and it would be guessing.
