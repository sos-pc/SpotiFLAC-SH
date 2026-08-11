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

**Phase 1 — make `present` mean something.** Wire `library-check-deleted` into a
scheduled pass and into `reconcile`. Nothing reads differently yet; the point is
that after this, the catalog's `status` has a writer for the first time.
*Verifiable:* rows change status when a file is deleted outside the app.

**Phase 2 — one resolver.** Extract the path resolution used by generation into
a single function, and route `syncDeletions`, `RemoveWatchlist` and
`recoverMissingFiles` through it. Deletion stops trusting `job.FilePath`.
*Verifiable:* rename a file in File Manager, remove it from the playlist, and
watch it actually get deleted — the case that leaked files forever.

**Phase 3 — take the scan out of the hot path.** With Phase 1 giving a
trustworthy index, `resolveTrackPaths` stops walking the library and reports
unresolved tracks instead. The walk moves into `reconcile`, where it is
explicit. *Verifiable:* generation time for the 2561-track playlist drops from
~15 s to sub-second; the `unresolved` count still appears, now as a diagnosis
rather than a trigger.

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

## 8. Open questions

1. **Does `job.FilePath` stay?** Phase 2 stops resolving against it. Keeping it
   as a breadcrumb costs nothing and helps forensics; removing it eliminates a
   registry that can drift. Recommendation: keep, stop trusting, revisit after
   Phase 3.

2. **How often should `reconcile` run unattended?** Phase 1 needs a cadence. A
   full verify is 2745 `os.Stat` calls — cheap — plus the tag walk only when
   repairing moves. Daily seems right; it is a settings decision.

3. **What happens to a track that never resolves?** Today it silently shortens
   the M3U8 and triggers the scan forever. Under Phase 3 it becomes a reported
   number. Whether `diagnose` should name the tracks is a UX question.

4. **`GetWatchlistsByUser` returns records with an empty `UserID` to every
   user.** Deliberate legacy accommodation or an access-control gap? Out of
   scope here, but it wants an answer.
