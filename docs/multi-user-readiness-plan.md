# Opening the instance to every Jellyfin user — plan

> **🧭 Plan, 2026-08-13.** The operator intends every Jellyfin account to use
> SpotiFLAC, not just the admin. This audits what that changes. It was triggered
> by a feature request — connect a Spotify account to pick playlists from a list
> — which is deferred to §8 because what it would expose is not ready to be
> shared. OAuth is required, not optional, by the operator's decision; §8a and
> §8b cover the user flow and the accessibility baseline it must be built
> against. Companions: [authentication.md](authentication.md) ·
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

**Fix before onboarding anyone.**

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
and lift their own confinement to the whole container filesystem.

**What that actually opens, checked route by route** — the first draft of this
section claimed it exposed the Bolt databases and the JWT signing secret. It
does not, and the difference matters:

| Route | Guard | What it does |
|---|---|---|
| `GET /api/v1/files` (`:277`) | `read` | **lists** a directory |
| `GET /api/v1/files/audio` (`:294`) | `read` | **lists** audio files in a directory |
| `GET /api/v1/files/metadata` | `v1RequireAdmin` | reads tags at an arbitrary path — correctly gated |
| every mutating file route (`:315`+) | `v1RequireAdmin` | delete/move/rename |
| `GET /api/v1/jobs/{id}/download` (`api_jobs.go:89`) | `manage` + owner check | serves `job.FilePath` — a **server-chosen** path, not a client one |

There is no arbitrary-file-read primitive for a non-admin. The two `read`
routes enumerate; they do not return contents. So the real exposure is:

- **Filesystem enumeration** anywhere in the container — layout, other users'
  library trees, `/staging`, `/tmp`.
- **Writes landing outside the intended library**, since the same value is the
  download destination. On the reference deployment `read_only: true` confines
  those writes to the mounted volumes, which still includes the config volume.

That is a boundary violation worth fixing before onboarding, not a secret
disclosure. Ranked first because it is the only finding here that a user can
trigger deliberately.

**Reproduced**, not reasoned about — a non-admin user, their own settings, and
the two functions above, with no HTTP involved:

```
racine de l'operateur          : C:\Users\Administrator\Music
racine appliquee a cet usager  : C:\
=> la racine de l'usager contient celle de l'operateur
ACCEPTE : C:\config\jobs.db     (refuse sous la racine operateur)
ACCEPTE : C:\etc\passwd         (refuse sous la racine operateur)
```

The point is not "a user chose a folder". It is that the root meant to
*contain* them ends up containing the operator's own — on the reference
deployment, everything under `/`, including the config volume mounted at
`/home/nonroot/.SpotiFLAC`.

Not exploitable today, because the only user is the admin — which is exactly
why it has to be fixed before that stops being true.

The probe that produced the output above should ship with the fix, inverted:
asserting that a user's override can no longer widen their root.

**Resolution — remove the class, do not contain it.** The first version of this
section proposed an operator-owned boundary that per-user overrides could only
narrow. §2a supersedes it: **make `downloadPath` instance-level**, writable by
an admin only. There is then no user-chosen root to validate, `libraryRootFor`
collapses back into `libraryRoot`, and the finding cannot recur.

That is less code than the boundary, and §2a shows it is also the *correct*
answer for a shared library rather than merely the cheaper one.

Keep one thing from the boundary idea: values already stored in Bolt from before
this ships must stop being honoured on read, not only rejected on write.

## 1b. Settings fork on first save, and instance keys fork with them

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
| `downloadPath` | see §2a — it decides where a file lands in a shared library |
| `folderTemplate`, `createPlaylistFolder`, `useFirstArtistOnly`, `filenameTemplate` | same, see §2a |

The rest — quality, `autoOrder`, tagging toggles — are personal. `autoQuality`
and the per-provider qualities are a special case; see §2b.

Worth saying plainly: making `spotFetchAPIUrl` resolve per user was a
*deliberate* change, and `EffectiveDownloadSettings`' own docstring
(`internal/settings/settings.go:132`) records it as a fix — four call sites were
reading the global file and "silently ignoring" the user's saved value. That was
correct when the only user was the operator. It is not a mistake being corrected
here; it is a decision that does not survive the new context.

### The defect underneath both of those

Resolution is a **replacement, not a merge**:

```go
if profile != nil && len(profile.Settings) > 0 {
	raw = profile.Settings          // the whole map, not the keys they set
}
if raw == nil {
	raw, _ = config.LoadSettingsFile()
}
```

One saved key means that user's map replaces the instance map entirely, and
every key they did not save resolves to its **zero value** rather than to the
operator's default.

Nothing has noticed because the frontend GETs the resolved view and PUTs it back
whole, so in practice every stored map is complete. The system is correct only
because the client behaves — a convention nothing states and nothing enforces.
The first API call written by hand with a single key blanks its author's entire
configuration.

### What else is wrong with the shape

- **No schema.** `map[string]interface{}`: no types, no validation, and the only
  way to learn which keys exist is to grep for `getString`/`getBool` calls. A
  typo is silent, a new key is declared nowhere.
- **Defaults live at the use sites.** `ParseDownloadSettings` yields zero
  values and each caller patches afterwards — `if base == "" { base =
  util.GetDefaultMusicPath() }`, `if filenameFormat == "" { filenameFormat =
  "title-artist" }`. "What is the default for this key" has no single answer.
- **No version stamp**, so a rename orphans the old key forever.
- **`job.Settings` has two lifetimes.** Watchlist jobs are refreshed from the
  watchlist at processing time (`internal/jobs/worker.go:91`); manual jobs keep
  the snapshot taken at enqueue. One field, two meanings, by origin.

## 2. Resolution: two typed stores, one layered read

Decided 2026-08-14 with the operator, over the cheaper option of keeping the
free-form map and adding a list of instance-scoped key names. That list would
have been a *second* source of truth about the keys, sitting beside the keys
themselves — the kind nobody updates. Scope is exactly what this deployment got
wrong; it should be a compile error, not a list.

**Three types, and the third is why this works.**

```go
// Stored in config.json. Admin-writable only. Complete: defaults applied.
type InstanceSettings struct { DownloadPath, FolderTemplate, … string }

// Stored per user in Bolt. SPARSE — every field optional.
type UserSettingsPatch struct { EmbedLyrics *bool; TidalQuality *string; … }

// Never stored. What callers read.
type Settings struct { … }
```

The pointers in the patch are the load-bearing detail. The whole point of
layering is to stop requiring the client to send every key; the moment it sends
a partial patch, "the user chose false" and "the user has no opinion" stop being
the same thing. Today they are indistinguishable, and `getBool` returns `false`
for a missing key — which is why no bool can currently default to true, and why
a user could never turn *off* a setting that did.

**The read is layered, each layer overriding only what it sets:**

```
defaults()  ←  InstanceSettings  ←  UserSettingsPatch (user-scoped keys only)
```

Defaults are declared once, in `defaults()`, and deleted from the use sites.

**The scope rule, stated once so new keys classify themselves:** anything that
decides where a file lands on the shared disk, or names shared infrastructure,
is instance. Everything else is user.

| Instance | User |
|---|---|
| `downloadPath`, `folderTemplate`, `createPlaylistFolder`, `useFirstArtistOnly`, `filenameTemplate` — §2a | qualities, `autoOrder`, `autoQuality`, tagging toggles, `createM3u8File`, `downloader` |
| `jellyfinMusicPath`, `spotFetchAPIUrl` — one shared Jellyfin, one shared fallback endpoint | |
| later: the Spotify application's client id — §8 | later: each user's Spotify refresh token — §8 |

Note that quality stays **user**-scoped and §2b is unchanged by this: the
setting is genuinely personal, it is its *effect* that is bounded by
deduplication. Scoping and policy are different questions and this does not
answer the second.

**On the wire, one flat object, not two.** `GET /api/v1/settings` keeps
returning resolved values at the top level — the existing frontend relearns
nothing — plus the list of keys this caller may write. That list is what lets
the settings screen grey out instance keys for a non-admin *and say why*, which
§7 and §8b both wanted anyway. `PUT` accepts a partial patch, routes instance
keys through `v1RequireAdmin`, and rejects the rest with a message naming the
key rather than silently dropping it.

**A `schemaVersion` int goes in the instance store**, so the next rename has
somewhere to hook a migration instead of orphaning a key.

### The migration is mandatory, and here is why

Read from the live deployment, 2026-08-14, read-only over SSH:

```
/srv/…/DockerFiles/Spotiflac/config/
  catalog.db  jobs.db  jobs.db.bak-20260811-160709  tidal_token.json
```

**There is no `config.json`.** `LoadSettingsFile` returns `(nil, nil)` when the
file is absent (`internal/config/config.go:44`), so the instance store is empty
and *every* setting currently resolves from the admin's Bolt profile. Two values
exist only there:

```
jellyfinMusicPath : /Multimedia/Musique/Spotiflac
spotFetchAPIUrl   : https://spotify.afkarxyz.fun/api
```

Ship the layered read without a migration and both become empty. The first one
silently stops M3U8 files landing where Jellyfin reads them — no error, nothing
in the log.

So: **on first start, promote.** For each instance-scoped key absent from the
instance store but present in an admin's profile, write it to the instance store
and log the promotion. Then strip instance keys from every user profile. Both
halves, in that order, idempotent — promoting without stripping leaves stale
copies that the next reader might reach for; stripping without promoting is the
data loss above.

*Also worth the operator knowing, found while reading the above:*
`spotFetchAPIUrl` points at a **third-party public server**. It is the fallback
used when the native Spotify client fails (`internal/service/metadata.go:96`),
so whenever Spotify's TOTP handshake breaks — which it has — track and playlist
URLs go to someone else's infrastructure. Inherited from upstream defaults, not
chosen. Becoming instance-scoped at least stops any user repointing it; whether
to keep it at all is a separate decision.

This section also creates the admin write path that §8 needs for the Spotify
client id, which today has nowhere to live.

## 2a. A shared library, fragmented by personal settings

This is why the table above changed, and it is the strongest argument in the
plan for a boundary the operator owns.

A file's location is not a property of the library. It is computed from the
settings of whoever enqueued the job:

```go
func (jm *JobManager) buildOutputDir(job *Job) string {
	s := job.Settings                    // snapshot of the enqueuing user's settings
	base := s.DownloadPath
	...
	sub := OutputSubfolder(s.FolderTemplate, s.CreatePlaylistFolder,
		s.UseFirstArtistOnly, ...)
```

And `checkFileExists` (`internal/jobs/helpers.go:303`) — which is the *entire*
deduplication mechanism, the thing that turns a job into `skipped` instead of
downloading again — looks in that directory, using that same user's
`FilenameTemplate`.

So deduplication is keyed on five personal settings. Two users who differ on
**any one of them** download the same FLAC twice, to two paths, into a library
Jellyfin will show as duplicates. It works today only because everyone inherits
one global value; it is correct by accident, not by construction.

**Resolution.** In a shared-library deployment, every key that determines where
a file lands is instance-level. That is the §2 table. The personal settings that
remain are the ones that do not touch the path.

If per-user libraries are ever genuinely wanted, that is a different product —
it needs its own catalog scoping and its own Jellyfin story, and it is not this
plan.

## 2b. Quality is shared in outcome, whatever each user asked for

A consequence of the same mechanism, called out separately because it is a
policy question rather than a bug.

The skip check asks "is there a file here", not "is there a file here at the
quality this user wants". So if one user fetches a track at `LOSSLESS`, the next
user watching the same playlist is skipped onto that file — their `HI_RES`
preference silently never applies to anything someone else got there first.

The catalog carries `quality_rank` (`library_files`), so an upgrade path may
already exist for some flows. **To establish before deciding**, not to assume.

Three defensible answers, and the operator picks: keep the first file and say so
in the UI; upgrade in place when a better rank is requested; or make quality
instance-level too, which is the honest reading if the library is shared. What
is not defensible is the current state, where a user sets a preference the
system quietly cannot honour.

## 2c. Records with no owner are visible to everyone

`GetWatchlistsByUser` (`internal/watcher/watcher.go:883`) returns a watchlist
when `pl.UserID == userID` **or `pl.UserID == ""`**. The same `!= ""` escape
appears in the SSE filter (`sse.go:192`) and on the job download route
(`api_jobs.go:102`).

That was the migration path from the pre-authentication era, and at one user it
is invisible. With several, every record created before auth existed is visible
to all of them.

**Resolution.** A one-time backfill assigning ownerless records to the admin,
after which all three `== ""` special cases can be deleted. Doing the backfill
without deleting the cases leaves the hole open for the next ownerless record;
deleting the cases without the backfill hides existing watchlists from their
owner. Both, in that order, in one change.

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

**Settled by the live compose (2026-08-13): `DISABLE_AUTH_ON_LAN=false`, and the
service publishes no `ports:` at all — it is reachable only through nginx on the
`swagstack_default` network.** Both halves of the exposure are therefore closed
today, and `TRUST_PROXY_HEADERS=true` is correct for a single proxy hop
(`ratelimit.go:84`).

So this section is not a fix, it is a decision to record: the middleware is dead
code on this deployment. Deleting it removes a class of problem permanently and
costs nothing; keeping it means keeping a code path whose only failure mode is
silent and severe. Recommendation: delete it, and if a headless client is ever
needed, give it a scoped API key — which already exists and is already
permission-scoped.

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

**OAuth is required, by the operator's decision (2026-08-13), not optional.**
An earlier draft framed it as a later phase behind a "declared profile" path.
It is now the engine of the default source, and the declared path survives with
a *different job* — see the three sources below. That is cleaner than the draft,
where the two competed for the same use.

### Three sources, one picker, entered from where the need arises

Account association is not a settings chore. It is the first step of the first
source, and it belongs where a user goes to add something to watch — the
Watchlist tab — not buried in Settings.

| Source | What it is for | Backed by |
|---|---|---|
| **My playlists** | the default | OAuth. `/v1/me/playlists`, which also returns `tracks.total` |
| **A profile** | watching *someone else's* public playlists — a friend, a curator | the anonymous token; profile search + the profile endpoint |
| **A URL** | a playlist shared by link that no search will find | today's path, kept |

When the account is not connected, the **My playlists** panel does not show an
error. It shows the connect button and one line saying what it unlocks. The
association happens without leaving the panel, and the user lands back on the
list — not on the home screen.

Settings keeps a mirror — *Connected as X*, Disconnect — to **manage** the
association, never to acquire it.

### Profile search, and why it is not for finding yourself

The operator asked for a search box instead of pasting a URL. Measured: the
`searchDesktop` operation already wired into this codebase
(`backend/spotify/metadata.go:1402`) returns a `users` section alongside tracks
and albums, carrying `displayName`, `id`, `uri` and an avatar at 64 and 300 px.
No OAuth needed.

But display names are not unique and ids are opaque:

```
"marc"       → MARCA (diariomarca), Marc (wh89i7prv934tvupeicfd8a3p),
               Marc (ng8r4defqi9ow2mfxpiksxe55), Marc C (hed4sev…), … 10 results
"methammer"  → methammer (methammer)                                 1 result
```

So search is *worse* than a URL for finding **yourself**, unless your handle is
rare — and it is moot anyway now that OAuth is required, since the connected
account already knows who you are. Its real value is the second source: finding
**other people's** profiles, where the URL is exactly what you do not have.

The avatar is the only discriminator between ten identical names. That has an
accessibility consequence — see §8b.

**Two levels, one identity.** Presenting "public profile" versus "OAuth" as a
choice asks the user a question they cannot answer. It is one thing — your
Spotify identity — at two levels:

- **Declared**: a profile, yours or someone else's. Public playlists only.
- **Verified**: your connected account. Everything, including private and
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
the server. **Cleared on the reference deployment**, which is served over TLS by
SWAG at `spotiflac.redstack.fr`; the redirect URI would be
`https://spotiflac.redstack.fr/api/v1/spotify/callback`.

Each deployment must register its own Spotify application; a shared client ID
would cap the entire project at Spotify's 25-user development-mode limit and
hang it on one person's developer account. Use Authorization Code + PKCE so no
client secret is stored; only refresh tokens are, per user.

The one UI detail that decides whether this works for anyone but its author: the
redirect URI must be displayed **pre-filled from the origin the admin is
actually on**, with a copy button, and must refuse to proceed with a clear
message when that origin is not HTTPS.

Out of scope for a first version: Liked Songs. They are not a playlist, have no
URL, and the entire watcher is keyed on `SpotifyURL`.

## 8a. The flow starts at an empty state that is currently a dead end

This is what a new Jellyfin user sees on their first visit to the Watchlist tab
(`frontend/src/components/WatchlistPage.tsx:474`):

> 👁 *No playlists are being watched.*
> *Add a Spotify playlist to start auto-syncing new tracks.*

It says what to do and offers **no way to do it** — no button, nothing. The user
has to go and find the control somewhere else. At one user, who wrote the app,
that is invisible. It is now the first screen of every person being onboarded,
and it is where the whole feature should begin: this empty state should carry
*Connect your Spotify account*, with the other two sources behind it.

**Bulk add must show its cost before it commits.** `AddWatchlist`
(`internal/watcher/watcher.go:678`) enqueues the entire playlist immediately, in
a goroutine, on add. Ticking twelve boxes therefore enqueues everything at once
— which, until §3 lands, is the easiest gesture in the application and also the
one that blocks the household's queue for hours. The picker's confirm step
should read *"12 playlists, ~3400 tracks"*, not *"Add"*.

**Failure paths to design, because these are what make a feature look broken:**

| Situation | What the user must see |
|---|---|
| Admin has not registered the Spotify app | why the button cannot work — not a greyed-out control |
| User declines authorisation on Spotify | a page that receives them, not a raw error |
| Refresh token later revoked | an offer to reconnect, not a silently empty list |
| Profile shares no public playlists | "this profile shares none publicly" — information, not failure |
| Search returns nothing | a reminder that display names are not unique |
| Playlist already watched | shown as such and not tickable — **per user**; another user watching it is not this user's business |

**A question the operator has to settle before onboarding, not after:** the UI is
entirely in English (`<html lang="en">`, all strings), and the household being
invited is French-speaking. That is not a defect — it is consistent with the
existing app — but it is a decision. Translation is a real project and does not
belong inside this feature.

## 8b. Accessibility: the current baseline, measured

The picker is the largest interactive surface this app will have gained in one
go. Building it the way the existing components are built would multiply an
existing problem, so here is the measured baseline.

| Measurement | Result |
|---|---|
| `eslint-plugin-jsx-a11y` in the config | **absent** — nothing catches any of this |
| clickable `<div onClick>` in `src/components` | **12**, across 6 files |
| …of those, with `role`, `tabIndex` or a key handler | **0** |
| icon-only buttons (`size="icon"`) | **40** |
| …of those, with `aria-label` or `title` | **0** |

Concretely, for the four status filters in the download queue — refactored twice
in #65 and #69 without this being noticed: they cannot be reached by keyboard at
all, so a user who does not use a mouse **cannot filter the queue**; a screen
reader does not announce them as actionable; and the active filter is conveyed
by a background tint with no `aria-pressed`, so its state does not exist for
assistive technology.

One thing is right, and became right by accident: status is no longer
colour-only. #69's `STATUS_LABEL` gave every badge a text label beside icons of
differing shape.

**Resolution — the lint rule first, before the picker is written.** Adding
`eslint-plugin-jsx-a11y` would have caught all 52 items above, and more to the
point it prevents the picker from adding to them. Remediating the 40 existing
buttons is a separate cleanup and should not gate this feature.

**What the picker itself must do**, none of which is optional:

- Real checkboxes with associated `<label>`s — shadcn ships one.
- Profile results as a list of choices, not `<div onClick>`.
- Avatar `alt` text carrying the **id**, not just the display name. With ten
  profiles called "Marc", `alt="Marc"` ten times conveys nothing, and the avatar
  was the only discriminator — so the list becomes unusable for anyone reading
  text rather than seeing images.
- The selection count in an `aria-live` region. "12 selected" changing silently
  is information that exists only for people who can see it.

## 9. Sequence

1. **§1, §1b, §2 and §2a together.** They are one change: two typed settings
   stores and a layered read. §1 and §1b are the symptoms, §2a is why that
   shape is the right one, and §2 is the mechanism — which also creates the
   admin write path everything later needs. The migration in §2 is not optional
   and has to land in the same change as the read it protects. §2c (the
   ownerless backfill) rides along; it is small and it touches the same
   question of who owns what.
2. **§3, with §4 falling out of it.** Establish first *why* `jobWorkers` is 1 —
   that answer decides whether fairness is enough or throughput must also be
   addressed. §8a's bulk add is unsafe to ship before this.
3. **§7, and the §8b lint rule.** Small, independent, and the lint rule has to
   land before the picker is written rather than after. **§5 is now a
   deletion**, not a fix — the bypass is off and unreachable on this deployment.
4. **§8.** OAuth and *My playlists* first, since it is the default source; then
   *A profile* behind the same interface; the URL path already exists. §2b
   (quality policy) needs an answer before the picker promises anything about
   quality.

## 10. Explicitly not in this plan

- Per-user quotas or storage limits. Fairness (§3) addresses the actual
  complaint — waiting behind someone else — without inventing policy the
  operator has not asked for.
- Splitting the library per user. The shared library is the point of a Jellyfin
  household.
- Rate-limiting the API. No evidence of a problem, and it would be guessing.
