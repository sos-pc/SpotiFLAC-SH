# Catalog refactor — handoff document

This file tracks the in-flight refactor that turns the SQLite catalog into the
long-term source of truth for what's on disk, complementing the existing
BoltDB queue. Read this top-to-bottom before continuing the work.

Last updated: end of session that pushed `c8d4eeb`.

---

## TL;DR for the next session

- Repository: **`sos-pc/SpotiFLAC-SH`** (fork of `spotbye/SpotiFLAC`).
- Working branch: **`kiro`**, head at `c8d4eeb` (commit 9 of the original 9-commit plan, plan complete).
- Plan status: 9/9 commits done. Waiting on user testing in production then a
  follow-up commit 10 (`/admin/library-match`) to recover ~2540 orphan files.
- Open issue parked for later: Qobuz fallback returning HTTP 401 on every
  request (musicdl.me proxy upstream change). Documented but not addressed.
- The catalog (SQLite) is **additive**: BoltDB still drives the live queue
  and the watchlist data model. Removing the catalog rolls back cleanly.

---

## What this user is trying to do

- Self-hosts SpotiFLAC-SH on a Linux box (Docker), behind Jellyfin auth.
- ~3000 tracks already on disk, downloaded across many months.
- Several watchlists pointing at Spotify playlists (incl. Discover Weekly).
- Wants Jellyfin-readable M3U8 files that always reflect the playlists,
  even after BoltDB job cleanup, even after manual file moves.
- Original problem: M3U8 files were nearly empty because the M3U8
  generator depended on either the in-file `SPOTIFY_ID` tag or a still-present
  BoltDB job, and ~2540 of his files had neither.

The whole catalog refactor was triggered by his question
"why are my M3U8 nearly empty?". Don't lose that thread.

---

## Architecture decision (validated by the user)

We agreed on:

1. **SQLite catalog** alongside BoltDB. New file:
   `<config>/catalog.db` (WAL, foreign keys ON, busy_timeout 5s).
   - Driver: `modernc.org/sqlite v1.40.0` — pure-Go, no CGO (the Dockerfile
     keeps `CGO_ENABLED=0`).
   - Migration runner: custom, ~150 LOC, embed-FS based,
     forward-only. See `backend/db/migrate.go`.
2. **Five entities** in the catalog:
   - `albums` — Spotify-side identity of an album.
   - `tracks` — Spotify-side identity of a track. FK to `albums(spotify_id)`.
   - `library_files` — physical files on disk. FK to `tracks(spotify_id)`.
     Partial unique index `(spotify_id) WHERE status != 'deleted'` enforces
     "at most one active file per track". Replacing keeps audit trail.
   - `download_attempts` — append-only log of every job processed by the
     queue. Replaces what BoltDB jobs used to play as long-term history.
   - `playlist_snapshots` + `playlist_snapshot_tracks` — versioned captures
     of each watchlist's contents. One snapshot per sync where the diff is
     non-empty (no-op syncs don't create rows).
   Plus the junction `watchlist_tracks` for "current state of each
   watchlist" — used directly by the M3U8 SQL JOIN.
3. **BoltDB unchanged** for: live job queue, `WatchedPlaylist` records,
   `users`, `apikeys`, `api_proxies`, `proxy_discovery`, `history` /
   `fetch_history`. Migration of those is out of scope for this plan.
4. **"Keep best quality"** rule: when a higher-quality download lands, the
   old `library_files` row is flipped to `status='deleted'` (audit trail)
   and a new row is inserted. Comparison is done via numeric `quality_rank`
   (HI_RES_LOSSLESS=4, HI_RES=3, LOSSLESS=2, HIGH=1).
5. **Tracks visible across users** (per user instruction): the catalog is a
   shared library. `library_files.downloaded_by` is informational.
6. **Previews not catalogued**: 30-second preview downloads stay out — the
   audio worker already only catalogs successful full-quality downloads.
7. **Forward-only migrations**: write a new SQL file to revert a change,
   never edit an applied migration.

Trade-offs we discussed and rejected:

- "Single fat `CatalogEntry` blob in BoltDB" — rejected as a god-object that
  would have required a refactor in <6 months.
- "Migrate everything to SQLite at once" — rejected as too invasive; agreed
  to do it progressively, BoltDB stays for state that mutates frequently.

---

## File layout added by this work

```
backend/db/
├── db.go                    # Open() + Close() with WAL + pragmas
├── migrate.go               # forward-only migration runner, embed.FS
├── migrations/
│   ├── README.md            # convention: NNNN_description.sql, ";\n" splits
│   ├── 0001_tracks_albums.sql
│   ├── 0002_library_files.sql
│   ├── 0003_download_attempts.sql
│   └── 0004_playlists.sql
├── helpers.go               # Querier interface, nullableString, boolToInt
├── quality.go               # Quality* + Status* constants, QualityRank()
├── albums.go                # Album DAO: UpsertAlbum, UpsertAlbumStub, GetAlbum
├── tracks.go                # Track DAO: UpsertTrack, UpsertTrackStub, GetTrack, GetTrackByISRC
├── library_files.go         # LibraryFile DAO + libraryFileIDPrefix "lib-"
├── download_attempts.go     # DownloadAttempt DAO + downloadAttemptIDPrefix "att-"
├── snapshots.go             # PlaylistSnapshot DAO + snapshotIDPrefix "snap-"
└── watchlists.go            # SetWatchlistTracks (atomic clear+insert with diff),
                             # IsTrackInOtherWatchlists, ListWatchlistTrackIDs

jobs.go                      # JobManager.catalog *sql.DB added
                             # NewJobManager(configDir, db, catalog) — signature changed
                             # EnqueueBatch calls checkCatalogDedup before saving job
jobs_catalog.go              # NEW: recordCatalogDone/Failed/Skipped called from worker,
                             # checkCatalogDedup + recordCatalogDedupSkip called from EnqueueBatch
jobs_worker.go               # 3 calls inserted at terminal transitions
watcher.go                   # resolveTrackPaths now: catalog → fs index → BoltDB legacy
                             # syncPlaylist calls mirrorWatchlistToCatalog after saveWatchlist
watcher_catalog.go           # NEW: mirrorWatchlistToCatalog + catalogPathsForWatchlist
api_admin.go                 # registerAdminRoutes wires /retag-legacy + /library-rebuild
container.go                 # Container.Catalog *sql.DB
main.go                      # opens catalogdb.Open(configDir), passes to NewJobManager
backend/meta/spotify_index.go # Exposed IsSupportedAudioExt + ReadSpotifyID for library-rebuild
go.mod                       # +modernc.org/sqlite v1.40.0 + 12 transitive deps
go.sum                       # regenerated by go mod tidy
docs/api-reference.md        # /admin/library-rebuild documented
.kiro/HANDOFF.md             # this file
```

Nothing else was renamed or deleted. The existing `meta.BuildSpotifyIDIndex`,
`meta.WriteSpotifyIDTag` and the BoltDB legacy code paths are still there and
still used as fallbacks.

---

## The 9 commits, in order, with intent

Commit hashes are post-attribution (the `github_push_to_remote` tool rewrites
SHAs with author metadata, so the values may differ slightly from local
hashes — the messages below are the source of truth).

### 1. `feat(db): add SQLite catalog layer with migration runner`
- Adds `backend/db/db.go` and `backend/db/migrate.go`.
- Wires `Catalog *sql.DB` into `Container` and opens it from `main.go`.
- No business schema yet, only `schema_migrations` table.
- Verified: container boots clean, `catalog.db` appears in config dir.

### 2. `feat(catalog): tracks and albums tables with DAO`
- Migration `0001_tracks_albums.sql` + Go DAOs.
- `UpsertTrack` auto-stubs the parent album row to keep FK satisfiable.
- "Not found is not an error" convention: `GetX` returns `(nil, nil)`.
- `GetTrackByISRC` for the recovery path.

### 3. `feat(catalog): library_files table and DAO`
- Migration `0002_library_files.sql`.
- Partial unique index enforces at most one non-deleted row per track.
- DAO: `CreateLibraryFile`, `GetActiveLibraryFile`, `MarkLibraryFileDeleted`,
  `UpdateLibraryFileStatus` (rejects `'deleted'` — caller must use
  MarkLibraryFileDeleted for that), `UpdateLibraryFilePath`.
- ID format `lib-<32 hex>` (matches existing `key-`, `att-` etc.).

### 4. `feat(catalog): download_attempts table and DAO`
- Migration `0003_download_attempts.sql`.
- Append-only log replacing what BoltDB jobs used to do for history.
- Statuses: `pending | downloading | done | failed | skipped | cancelled`.
- DAO: `CreateDownloadAttempt`, `SetDownloadAttemptDownloading`,
  `MarkDownloadAttemptDone/Failed/Skipped`, `GetDownloadAttempt`,
  `ListDownloadAttemptsBySpotifyID`. Const SQL fragment shared between
  Get and List so columns can never drift between scan paths.

### 4.5. `chore(deps): go mod tidy after adding modernc.org/sqlite`
- Required because the CI test job in `.github/workflows/docker.yml` runs
  `go test ./... -v` before any `go mod tidy` step. Without committing
  go.sum, the test job fails on "missing go.sum entry for module".

### 5. `feat(catalog): playlist snapshots and watchlist_tracks junction`
- Migration `0004_playlists.sql`. Three tables:
  - `watchlist_tracks` — (watchlist_id, spotify_id, position, added_at)
    PRIMARY KEY (watchlist_id, spotify_id). No FK on `watchlist_id`
    because watchlists themselves stay in BoltDB.
  - `playlist_snapshots` — id, watchlist_id, playlist_name (frozen at
    snapshot time, so renames don't lose history), track_count, taken_at,
    added_count, removed_count.
  - `playlist_snapshot_tracks` — (snapshot_id, spotify_id, position).
    `ON DELETE CASCADE` on snapshot_id.
- DAO: `SetWatchlistTracks` (atomic clear-then-insert, returns the
  `(added, removed)` diff so the watcher can decide whether to snapshot),
  `IsTrackInOtherWatchlists` (one-row COUNT for sync_deletions
  multi-playlist protection), `ListWatchlistTrackIDs` (ordered IDs for
  M3U8), `RemoveWatchlistTracks` (full delete on watchlist removal),
  `CreatePlaylistSnapshot`, `LatestSnapshotID`, `ListPlaylistSnapshots`,
  `GetSnapshotTrackIDs`.
- `UpsertTrackStub` was added to `tracks.go` so the watcher can reference
  a track ID before fetching its full metadata.

### 6. `refactor(jobs): mirror terminal job transitions into catalog`
- New file `jobs_catalog.go` containing **best-effort** mirror helpers
  called by the worker after `saveJob/notifyJob`:
  - `recordCatalogDone(j *Job)` — UpsertTrack + upsertActiveLibraryFile
    (keep-best logic via FilePath comparison) + DownloadAttempt(done).
  - `recordCatalogFailed(j *Job)` — UpsertTrack + DownloadAttempt(failed).
  - `recordCatalogSkipped(j *Job)` — UpsertTrack + DownloadAttempt(skipped).
    Deliberately doesn't create a library_file: the worker can't tell who
    produced an existing on-disk file. library-rebuild handles that path.
- All three are no-ops if `jm.catalog == nil`. Errors are logged with a
  `[Catalog]` prefix and never block the queue.
- `JobManager` got a `catalog *sql.DB` field; `NewJobManager`'s signature
  gained a `catalog` parameter. Only caller is `main.go`.
- **Known gap**: `Job` doesn't carry a Spotify album ID, so `Track.AlbumID`
  stays empty. Plumbing it through is a separate commit (probably commit 11).
- `deriveCatalogQuality` maps per-provider settings into the canonical
  vocabulary. For `auto` downloads, it returns the *user's intent* (from
  `autoQuality`), not which provider actually won — `DownloadResponse`
  doesn't surface that information today.

### 7. `feat(jobs): dedup via catalog before enqueue`
- Adds `checkCatalogDedup` and `recordCatalogDedupSkip` to `jobs_catalog.go`.
- `EnqueueBatch` calls `checkCatalogDedup` after the existing
  active-jobs dedup. Skip rules: catalog is set, library_file row exists,
  `os.Stat` on `FilePath` succeeds, `quality_rank >= requested_rank`.
- Stale catalog rows pointing at deleted files DO NOT block re-download
  (the Stat check fails → dedup returns skip=false → worker re-DLs).
- Quality upgrade flow: if user switches `autoQuality` from 16 to 24 and
  the catalog has a 16-bit copy, dedup returns skip=false, the worker
  downloads the 24-bit version, recordCatalogDone marks the old row
  `deleted` and inserts the new one. Verified by reading the code, not
  by integration test.

### 8. `refactor(watcher): mirror watchlist state into catalog and resolve M3U8 from SQL`
- `watcher_catalog.go` introduced with two best-effort helpers:
  - `mirrorWatchlistToCatalog(pl *WatchedPlaylist)` — UpsertTrackStub for
    each track_id, then `SetWatchlistTracks` (returns diff), then a snapshot
    only if first-ever or diff is non-empty.
  - `catalogPathsForWatchlist(pl)` — single SQL query
    `SELECT spotify_id, file_path FROM library_files WHERE status = 'present'
    AND spotify_id IN (?,?,...)`.
- `syncPlaylist` calls `mirrorWatchlistToCatalog(&pl)` immediately after
  `saveWatchlist` and BEFORE the rename + M3U8 regeneration block.
- `resolveTrackPaths` is now a **3-tier fallback chain**, in order:
  1. catalog `library_files` (with `os.Stat` verification of the recorded
     path — falls through to the next source on stale rows).
  2. filesystem `BuildSpotifyIDIndex` (the existing tag-walk; lazy — only
     built if at least one ID is missing from the catalog).
  3. BoltDB legacy `legacyJobPaths` (unchanged).
- The **lazy filesystem walk** is what makes the new path strictly faster
  than the old one once the catalog is populated: a M3U8 regeneration on a
  fully-catalogued library does ZERO filesystem walks.

### 9. `feat(api): POST /admin/library-rebuild — scan filesystem into catalog`
- Two new public functions in `meta/spotify_index.go`:
  `IsSupportedAudioExt(ext)`, `ReadSpotifyID(path)`. Both use the same
  format dispatch as `BuildSpotifyIDIndex`.
- Handler walks every configured `downloadPath` (union of all watchlist
  `Settings.DownloadPath` + global default), reads tags, and ingests:
  - **imported** = brand new library_file row.
  - **verified** = existing row at same path, `last_verified_at` bumped.
  - **moved** = existing row updated to a new path.
  - **no_tag** = file lacks the SPOTIFY_ID tag — candidate for
    `/admin/library-match` (commit 10, not yet implemented).
  - **failed** = catalog write or read failed (logged).
- `provider="unknown"`, quality conservative (FLAC/M4A → LOSSLESS, MP3 →
  HIGH) so a future hi-res download can still beat checkCatalogDedup and
  upgrade in place.
- 10-minute global timeout, idempotent.
- Response includes `no_tag_sample` (first 50 paths) so the operator can
  confirm the scan walked the right tree without bloating the JSON.

---

## What's done, what's pending, what's blocked

| Status | Item |
|---|---|
| ✅ done | SQLite layer + 5 entities + DAOs |
| ✅ done | Worker writes terminal transitions to catalog |
| ✅ done | EnqueueBatch dedups against catalog |
| ✅ done | Watcher mirrors state + creates snapshots |
| ✅ done | M3U8 generator queries catalog first, fs second, BoltDB last |
| ✅ done | `/admin/library-rebuild` ingests tagged files into catalog |
| ⏳ user testing | All 9 commits pushed to `kiro` branch, waiting on user to deploy and report |
| 🔜 next | Commit 10: `feat(api): POST /admin/library-match` — fuzzy-match orphan files against Spotify metadata of watchlist tracks |
| 💤 deferred | Commit 11+: plumb Spotify album ID through Job → Track.AlbumID linkage |
| 💤 deferred | Migrate watchlists / users / api_keys from BoltDB to SQLite (Phase 2 of the original architecture decision) |
| 🐛 parked | Qobuz fallback returns HTTP 401 from `musicdl.me` for every track (separate from this refactor — likely upstream X-Debug-Key derivation drift) |

---

## Commit 10 — `library-match` design (validated direction, not coded)

Goal: recover the ~2540 orphan files that have no SPOTIFY_ID tag and aren't
referenced by any BoltDB job either. These are the "ghost" files dragging
M3U8 completeness down.

User accepted **Option B** (separate admin endpoint) over Option A (auto in
syncPlaylist). The reasoning: matching is risky (false positives on
duration arrondis, multiple versions, regional editions); having a
dedicated endpoint with a dry-run keeps the watcher fast and lets the
operator review before tags are written.

Proposed `POST /api/v1/admin/library-match`:

**Request**:
```json
{
  "watchlist_id": "watch-...",   // optional: scope to one watchlist
  "dry_run": true                 // optional, default false
}
```

**Algorithm** (per watchlist):
1. Get `pl.TrackIDs` from BoltDB.
2. For each `spotify_id` not already in catalog with status='present':
   - Fetch Spotify metadata (Title, Artist, Album, Duration, ISRC).
3. Walk filesystem, for each file with no SPOTIFY_ID tag:
   - Read tags via FFprobe (Title, Artist, Album, Duration_ms, ISRC if present).
4. Match each Spotify track against orphan files using priority:
   - ISRC exact match → automatic accept (rare but definitive).
   - Title (normalized) + Artist (first artist) + Duration ±2s → accept.
   - Title + Artist + Album → accept with warning.
   - Otherwise → no_match.
5. If `dry_run=false`: write the SPOTIFY_ID tag (`meta.WriteSpotifyIDTag`)
   and ingest into catalog (`UpsertTrack` + `CreateLibraryFile`).

**Response**:
```json
{
  "scanned_files": 3000,
  "scanned_tracks": 1850,
  "matched":   2487,
  "tagged":    2487,    // 0 if dry_run
  "ambiguous": 12,      // multiple files match the same track
  "ambiguous_files": ["..."],
  "no_match":  51,
  "no_match_track_ids": ["..."]
}
```

Normalization to apply on Title and Artist before comparing:
- lowercase, NFKD-decompose, strip combining marks, remove `(...)` `[...]`,
  collapse whitespace, trim.

The user's library has multi-artist strings ("Seekae, Flume"). Take only the
first artist for matching (`useFirstArtistOnly` is already an existing
setting whose semantics match exactly). Splitter on `, ` `&` ` feat. ` ` ft. ` ` featuring `.

---

## Sandbox pitfalls observed during this work

These are tool-level quirks of the Kiro sandbox, not Go quirks. Re-record
them so the next session doesn't waste time on them:

1. **Path resolution of `fs_write` / `create`**: relative paths sometimes
   resolve to `/projects/sandbox/...` rather than
   `/projects/sandbox/SpotiFLAC-SH/...`. Always use absolute paths starting
   with `/projects/sandbox/SpotiFLAC-SH/`.
2. **Go is not in PATH** by default. Get it via:
   ```bash
   curl -sSL https://go.dev/dl/go1.26.3.linux-amd64.tar.gz \
     -o /projects/sandbox/.go-toolchain/go.tgz
   tar -xzf /projects/sandbox/.go-toolchain/go.tgz -C /projects/sandbox/.go-toolchain/
   export PATH=/projects/sandbox/.go-toolchain/go/bin:$PATH
   export GOPATH=/projects/sandbox/.go-toolchain/gopath
   export GOCACHE=/projects/sandbox/.go-toolchain/gocache
   export GOMODCACHE=/projects/sandbox/.go-toolchain/gomodcache
   ```
   `.go-toolchain/` lives at workspace root, outside the repo, so `git status`
   ignores it naturally.
3. **`go vet ./... && go build ./...`** with `frontend/dist/.gitkeep` placeholder
   is the minimum sanity check before every commit:
   ```bash
   mkdir -p frontend/dist && touch frontend/dist/.gitkeep
   go vet ./... && go build ./...
   rm -f frontend/dist/.gitkeep && rmdir frontend/dist 2>/dev/null
   ```
4. **`mcp_sandbox_github_push_to_remote`** is the only way to push (gateway
   refuses raw `git push` due to header injection). It has no
   `--delete` equivalent — branch deletion must happen via GitHub UI.
5. **`run_command` tool sometimes 404s mid-session**. Use `execute_bash`
   with an explicit `cwd` parameter as fallback (don't use `cd`, the tool
   rejects it).
6. **`go mod tidy` MUST be committed**. The Dockerfile runs it before
   `go build`, but the CI test job (`.github/workflows/docker.yml` →
   `go test ./... -v`) runs first, before any tidy step, and fails on
   "missing go.sum entry" if go.sum isn't current.

---

## Test plan (when the user comes back)

Pre-conditions: container running with the latest `kiro` build (or
`docker compose pull && docker compose up -d` on `:kiro` tag).

1. **Boot logs** show `[DB] migration 000{1..4}_xxx applied` (only on the
   first run with this code; subsequent boots are silent on DB).
2. **`<config>/catalog.db`** exists (~32 KB initially), with `catalog.db-wal`
   and `catalog.db-shm` next to it (WAL artifacts, normal).
3. **A new download** writes 1 row in each of `tracks`, `library_files`,
   `download_attempts(status=done)`. Test with:
   ```sql
   sqlite3 /path/to/catalog.db "SELECT COUNT(*) FROM tracks;"
   sqlite3 /path/to/catalog.db "SELECT COUNT(*) FROM library_files WHERE status='present';"
   sqlite3 /path/to/catalog.db "SELECT spotify_id, status, provider FROM download_attempts ORDER BY started_at DESC LIMIT 5;"
   ```
4. **Re-trigger the same download** → log line
   `[Jobs] Catalog dedup: <track> — already in library at LOSSLESS (rank 2) ≥ requested rank 2`
   and a new `download_attempts(status=skipped)` row.
5. **A watchlist sync** writes:
   - `watchlist_tracks` rows (one per current track).
   - On first sync OR diff non-empty: a new `playlist_snapshots` row +
     `playlist_snapshot_tracks` rows.
6. **`POST /admin/retag-legacy`** → tags ~447 files (the BoltDB Done/Skipped
   jobs that still have files on disk). Already verified in earlier session.
7. **`POST /admin/library-rebuild`** → the user's expected output:
   ```json
   {
     "files_scanned": 3000,
     "imported":      ~459,    // tagged files (post-retag + new DLs)
     "verified":      0,
     "moved":         0,
     "no_tag":        ~2541,
     "failed":        0
   }
   ```
   The `imported` count is what the M3U8 generator can now reach from the
   catalog; `no_tag` is the work for commit 10.
8. **Sync a watchlist** → M3U8 file fills with the catalog hits (better than
   before but not yet complete). After commit 10 this becomes complete.

If anything writes 0 rows in a step that should have written some, the
`[Catalog]` log lines explain why — never silent.

---

## Open questions to clarify with the user before commit 10

- **Confirm Option B over Option A** for library-match. They said B but
  re-confirm before coding 200 LOC.
- **Dry-run by default?** I'd default `dry_run=false` for symmetry with
  `retag-legacy`, but a first-time user might prefer dry-run by default
  to preview matches.
- **Ambiguous matches**: when two files match the same Spotify track,
  pick the higher-quality one (FLAC > M4A > MP3, larger size > smaller)?
  Or skip and report?
- **Spotify metadata fetch**: synchronous per-track or pre-fetch a batch?
  3000 tracks at ~9 req/min (Spotify rate limit) = ~5 hours. Need to think
  about caching across calls. Probably reuse `spotify.GetFilteredSpotifyData`
  per playlist (already paginated by upstream code).

---

## Important context the next session might miss

- The user is **French-speaking** but commit messages and code comments are
  in English. Match that style.
- Steering rules in `~/.kiro/steering web general.md`: conventional commits,
  one logical change per commit, max 40 LOC per function unless justified,
  guard clauses first, no nested ternaries, no silent error handling.
  These have been respected throughout this work.
- The user explicitly asked to "stop, present 2-3 options with trade-offs,
  wait for explicit choice" on hard-to-reverse decisions. Don't skip that.
- The user **does test in production** (his self-hosted box) and reports
  back logs. Don't assume tests pass — wait for him to confirm.
- The Qobuz 401 issue is **NOT** part of this refactor. Don't get pulled into
  fixing it until the catalog work is fully landed and validated.
- The CI workflow on `kiro` branch builds `:kiro` tag automatically on
  every push — so the user just needs `docker compose pull && up -d`.
- LAN bypass works on his setup (`DISABLE_AUTH_ON_LAN=true`), so curl from
  the host doesn't need a token. From elsewhere, JWT or API key required.

---

## Minimal command reference

```bash
# Get a JWT (admin path, on the host with LAN bypass enabled)
curl -X POST http://<HOST>:6890/api/v1/auth/local | jq -r .token

# Or with credentials
TOKEN=$(curl -s -X POST http://<HOST>:6890/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"pwd"}' | jq -r .token)

# Run the two admin endpoints, in order
curl -X POST http://<HOST>:6890/api/v1/admin/retag-legacy   -H "Authorization: Bearer $TOKEN" | jq
curl -X POST http://<HOST>:6890/api/v1/admin/library-rebuild -H "Authorization: Bearer $TOKEN" | jq

# Inspect catalog directly (read-only is safe even while server is up, WAL allows concurrent reads)
docker compose exec spotiflac sh -c \
  'apt-get -qq install -y sqlite3 >/dev/null 2>&1; sqlite3 /home/nonroot/.SpotiFLAC/catalog.db ".tables"'
```
