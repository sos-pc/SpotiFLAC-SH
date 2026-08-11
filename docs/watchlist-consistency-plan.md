# Making the catalog trustworthy — plan

> **🧭 Plan, 2026-08-11.** Written after an audit of `internal/watcher` that
> produced 28 findings; the seven real bugs among them shipped in #50–#53 and are
> not repeated here. This plan covers what is left, which is one problem wearing
> four faces. Every number below is measured on the reference deployment, with
> the command that produced it inline. Companions:
> [watchlist.md](watchlist.md) · [api-redesign-plan.md](api-redesign-plan.md).

## 0. The finding

The system has a source of truth for "where is the file for this Spotify track".
It was chosen deliberately, it is the right one, and **nothing was ever written
to maintain it**.

`library_files.status` has a five-state lifecycle — `present`, `missing`,
`moved`, `corrupt`, `deleted` (`backend/db/quality.go:17-21`). The comment on
`v1CheckDeletedFiles` records what became of it:

> Three comments in the codebase refer to a "rescan task" that marks files
> missing. That task does not exist. `StatusMissing` was written only by tests,
> so all 2589 rows in prod claimed "present" without anything ever having
> verified it.

Every consumer that needed a file path met a catalog that could not be trusted,
and each grew its own fallback. Those fallbacks became independent registries.
The registries drift. Everything else in this plan is a consequence.

**This is not a proposal to pick a source of truth. It is a proposal to finish
maintaining the one already picked, and delete what grew in its absence.**

## 1. What records a file's location today

Four registries. `syncCatalogPathOnRename` names them itself — it exists to
mirror a rename into *"every store that independently records the file's path"*
(`internal/service/rename_catalog.go:33`):

| Registry | Written by | Authority claimed |
|---|---|---|
| SQLite `library_files.file_path` | `recordCatalogDone`, `library-rebuild` | *"source of truth once populated"* (`watcher.go:2347`) |
| BoltDB `jobs[].FilePath` | the job worker, rename sync | — |
| BoltDB `DownloadHistory[].Path` | history, rename sync | — |
| The file's own `SPOTIFY_ID` tag | the tagger at download | *"the filesystem is the source of truth"* (`watcher.go:1895`) |

A third comment adds *"[BoltDB is the] source of truth, the catalog is the
long-term audit trail"* (`catalog.go:45`) — the exact inverse of the first.

And consumers disagree about which to believe:

| Function | Resolution order |
|---|---|
| M3U8 generation (`resolveTrackPaths`) | catalog → **full filesystem tag scan** → jobs |
| `syncDeletions` | jobs only |
| `RemoveWatchlist` | jobs only |
| `recoverMissingFiles` | jobs only |
| `GetWatchlistStats` | catalog, then jobs |

Generation trusts the catalog. Deletion trusts jobs. Two answers to one
question, and the code already knows what that costs: `UpdateJobFilePathsForRename`
was added because a File Manager rename updated only the catalog, so
`os.Remove(job.FilePath)` used a stale path, failed silently, and *"leaked the
actual (renamed) file on disk forever"* (`internal/jobs/storage.go:80`).

The response was to add a write-through, not to remove a registry. That
write-through is explicitly *"best-effort throughout: errors are logged, never
returned"* — so divergence between the four is **silent by design**.

## 2. Measurements

```bash
# library size and shape
ssh BobyNAS 'find /data/Multimedia/Musique/Spotiflac -type f \
  \( -iname "*.flac" -o -iname "*.mp3" -o -iname "*.m4a" \) | wc -l'
```

2745 audio files: 2744 FLAC, 1 MP3, no M4A — so `BuildSpotifyIDIndex` never
forks `ffprobe` here, every read is the native FLAC path.

```bash
# walk vs tag reads, cold then warm
ssh BobyNAS 'cd /data/Multimedia/Musique/Spotiflac
  t=$(date +%s%3N); find . -type f -iname "*.flac" > /tmp/a; echo $(( $(date +%s%3N) - t )) ms
  head -400 /tmp/a > /tmp/s
  t=$(date +%s%3N); while read f; do head -c 65536 "$f" >/dev/null; done < /tmp/s
  echo $(( $(date +%s%3N) - t )) ms'
```

| | |
|---|---|
| Directory walk alone | **104 ms** |
| Tag reads, cold cache | **~15.4 s** extrapolated over 2744 |
| Tag reads, warm cache | **~2.5 s** |

BoltDB, counted by opening the copied file read-only and walking each bucket:

| Bucket | Keys | Bytes |
|---|---|---|
| `jobs` | 164 | 218 KB |
| `watchlist` | 3 | 73 KB (68 KB of it one record) |

**The scan is the only thing here that costs real time**, and it is the
arbitration mechanism: the comment on `syncCatalogPathOnRename` names it as the
fallback for when the stores diverge. Fifteen seconds, on every generation, to
settle a disagreement between four registries that should not disagree.

It runs whenever `len(validCatalog) < len(trackIDs)`, and
`catalogPathsForWatchlist` only returns rows at `status='present'` — which a
track that was never downloaded never has. So **one undownloaded track in a
playlist rebuilds the whole index on every generation**: after each sync, after
each batch, and once per watchlist at startup.

## 3. The decision

**The filesystem is the truth. The catalog is its index, and the index is
maintained rather than guessed at. Nothing else records a location.**

This is what the code already says in two of its three claims. What is missing
is the maintenance, and the deletion of the substitutes.

## 4. What each store becomes

**The filesystem** — authoritative, by construction. A file is there with its
`SPOTIFY_ID` tag or it is not. Never consulted in a hot path.

**`library_files`** — the index. Every path answer comes from here. Its `status`
is maintained by an explicit reconcile, so `present` means something. Consumers
read it and do not second-guess it.

**BoltDB `jobs`** — the record of what was *attempted*: status, error, timing,
provider. **`FilePath` stops being an authority.** It may stay as a breadcrumb
for the legacy fallback during migration, and is removed from every resolution
path.

**`DownloadHistory`** — a user-facing log. Never resolved against.

The consequence worth stating plainly: **deletion resolves its paths exactly the
way generation does.** That single change removes the class of bug that
`UpdateJobFilePathsForRename` was written to patch.

## 5. The API: ten actions become three verbs

Counted from the route table, there are ten ways to make the system "right",
with no stated hierarchy and overlapping implementations:

`POST /watchlists/{id}/sync` · `POST /watchlists/{id}/repair` ·
`GET /watchlists/{id}/freshness` · `GET /watchlists/{id}/stats` ·
`POST /admin/retag-legacy` · `POST /admin/library-rebuild` ·
`POST /admin/retag-incomplete-metadata` · `POST /admin/library-check-deleted` ·
`POST /admin/library-redownload-missing` · plus `checkM3U8Integrity`, which runs
at every startup for every watchlist with no endpoint and no visibility.

They overlap by containment: `repair` performs `retag-legacy` + `library-rebuild`
+ a forced regeneration; `freshness` calls `stats` and then re-resolves every
path; `sync` and `repair` both end in a regeneration; `checkM3U8Integrity` is
`repair`'s third step applied to everything, silently, on every boot.

Three verbs, separated by **what they change**:

**`sync`** — changes the *intention*. The only verb that talks to Spotify and
decides what should exist. Unchanged in spirit.

**`reconcile`** — aligns the *index* with the filesystem. The ladder
retag → rebuild → verify-status, with an explicit scope (`watchlist`, `root`,
`all`). The five admin endpoints become scopes of this one. This is the only
place the filesystem is walked, and it is walked because someone asked.

**`diagnose`** — read-only, one report, **one denominator**. `stats` and
`freshness` merge; every number is a partition of the same total.

`checkM3U8Integrity` disappears: it is an unrequested global `reconcile` on every
boot.

### 5.1 The counters, while we are here

The playlist card mixes three coordinate systems that share field names:

| Source | `downloaded` / `skipped` / `failed` mean |
|---|---|
| `SyncLog` | what **one batch** did — a delta |
| `WatchlistStats` | a **partition of the playlist** — always sums to `total_tracks` |
| `WatchlistFreshnessReport` | a **diff against Spotify** |

All three are rendered together, which is why nothing adds up. A second cause is
independent of the naming: `GetWatchlistStats` checks the catalog *first* and
`continue`s, so a track whose latest job **failed** but whose file exists counts
as `Downloaded` — while the History tab lists the failure. Both numbers are
correct in their own frame.

`diagnose` returning one report with one denominator resolves this by
construction.

## 6. Sequencing

Each phase ships on its own and is verifiable in production before the next
starts. No phase requires the one after it.

> **Status, 2026-08-11.** Phases 1–3 are merged (#55, #56, #57). The substance
> is done: the index has a writer, generation no longer reads the library to
> place a track, and one resolver answers for every consumer. Phases 4 and 5 are
> the surface — the API shape and the counters — and were always worth doing
> only after the substance held.

**Phase 1 — make `present` mean something.** Wire `library-check-deleted` into a
scheduled pass and into `reconcile`. Nothing reads differently yet; the point is
that after this, the catalog's `status` has a writer for the first time.
*Verifiable:* rows change status when a file is deleted outside the app.

**Phase 2 — take the scan out of the hot path.** `resolveTrackPaths` stops
walking the library and returns the IDs it could not place, so an unresolved
track becomes something to act on rather than a reason to read 2744 files.
*Verifiable:* generation for the 2561-track playlist no longer spends ~15 s on a
walk, and the warning names the tracks.

> **Swapped with Phase 3 on 2026-08-11, before either was written.**
>
> The original order had deletion adopt generation's resolver first. But
> agreeing on where a file is means reading the same sources — so deletion would
> have inherited the 15-second walk that the *next* phase removes. A regression
> scheduled on purpose is still a regression.
>
> Measured before swapping, on the reference deployment: across three watchlists
> and 2621 tracks, 12 had no catalog row; 11 of those were covered by a job
> record and the twelfth resolved to nothing at all. The walk placed **zero**
> tracks. Removing it first costs nothing and makes the merge below free.

**Phase 3 — one resolver.** Route `syncDeletions`, `RemoveWatchlist` and
`recoverMissingFiles` through the same resolution generation uses. Deletion
stops trusting `job.FilePath`. *Verifiable:* rename a file in File Manager,
remove it from the playlist, and watch it actually get deleted — the case that
leaked files forever.

**Phase 4 — three verbs.** Collapse the ten endpoints. Old routes stay as thin
redirects for one release. `checkM3U8Integrity` is removed.

**Phase 5 — one denominator.** Merge `stats` and `freshness` into `diagnose`,
and update the card.

Phases 1–3 are the substance. 4 and 5 are the surface, and are worth doing only
after the substance holds.

## 7. What this plan does not do

**No sweep of `/Playlists`.** One destroyed five real playlists on 2026-08-09
(reverted in #38). No filename rule separates an orphan from a file the user
made. Orphans get *reported*, never removed automatically.

**No cache with invalidation for the tag index.** It was considered and
rejected: a stale index right after a download is exactly when it misleads, and
the shrink guard would then skip the write. Phase 3 removes the need instead.

**No removal of the `SyncLogs` "standalone entry" fallback.** An earlier
inventory entry claimed #50 made it redundant; that was wrong. Its stated cause
is the 20-entry cap evicting a long batch's original entry, which is independent
of the stale-save race #50 fixed. It stays.

**No French/English comment normalisation.** 99 of 615 comments in `watcher.go`
are French. Fixing that alone rewrites the `git blame` of the whole file for no
functional gain; it happens where code is being rewritten anyway.

## 8. Decisions

Answered 2026-08-11. Recorded here because each one changes what gets built.

**`job.FilePath` stays, and stops being trusted.** Phase 2 routes every
resolution through the index; the field keeps being written because knowing
where a download put its file is worth having when something disappears. The
risk is that someone reads it and resolves against it again — the comment on the
field says not to, and Phase 2's single resolver is the thing that makes it
unnecessary.

**`reconcile` runs daily, unattended.** A full verify is 2745 `os.Stat` calls
against a 104 ms directory walk — cheap enough that daily costs nothing, and it
bounds how long a file deleted outside the app can keep claiming `present`. The
tag walk still only happens when repairing moves, not on the verify pass.

**`diagnose` names the unresolved tracks, not just their count.** "1 unresolved"
is the message that has been in the logs for two days and it is not actionable;
"1 unresolved: <title> — <artist>" would have let the operator remove the
offending track in ten seconds instead of paying the 15-second scan on every
generation since. The title is already in the catalog or in the Spotify payload.

**An ownerless watchlist becomes impossible to create.** `AddWatchlist` rejects
a request with an empty `UserID`, and the `|| pl.UserID == ""` clause in
`GetWatchlistsByUser` goes with it.

### 8.1 Where ownerless records came from

Worth recording, because the answer says the hole is already closed at its
source and this is cleanup rather than defence.

`POST /api/v1/watchlists` stores `userIDFromContext(r)`, which is empty only when
the caller's claims carry no `UserID`. On the reference deployment three of four
API keys have `user_id: ""` — `admin-cli` (11:00:38), `admin-cli-2` (11:05:05)
and `dev` (11:24:05), all created 2026-07-13. The fourth, created two days
later, has one.

`caa44ee`, committed 2026-07-13 at **11:40:08**, explains the gap:
`GetOrCreateUser` refreshed a returning profile's name and admin flag but never
re-derived its `ID` from the BoltDB lookup key, so a profile written before that
field existed stayed permanently `ID=""` — and *"any API key they created
inherited UserID='' from JWTClaims"*. All three keys predate the fix by under
forty minutes.

The same commit notes the other half: `ValidateAPIKey` calls `GetUser("")`, which
always fails, so those keys were *"silently downgrading every admin-scoped key
they ever created to non-admin"*. They have claimed permissions they do not get
for a month.

So no new ownerless key can be minted today. What remains is three stale keys
that can still create ownerless watchlists, and two rules that disagree about
what to do with one: `GetWatchlistsByUser` shows it to everyone, while
`checkWatchlistOwnership` lets only admins touch it. The decision above settles
that by removing the case rather than reconciling the two rules.

## 9. Still open

**Whether the three ownerless API keys are deleted or re-owned.** Neither is
required by the plan — the `AddWatchlist` guard makes them harmless either way —
and it is the operator's call. They last authenticated on 2026-07-13.
