package main

// ─────────────────────────────────────────────────────────────────────────────
// Catalog mirroring + lookup for the watcher.
//
// The watcher mirrors every successful sync into the SQLite catalog so the
// long-term playlist state survives BoltDB cleanup, and reads from the
// catalog to resolve M3U8 file paths. All catalog calls here are
// best-effort: errors are logged with a [Catalog] prefix and the watcher
// continues on its existing fallback paths.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
)

// catalogMirrorTimeout caps how long the watcher waits on the catalog
// during a sync mirror. The work is bounded (one stub per track + one
// transactional SetWatchlistTracks + one optional snapshot insert) so
// 10 s is generous; the timeout exists for pathological cases (disk
// stalls, lock contention) so a sick catalog never wedges the daemon.
const catalogMirrorTimeout = 10 * time.Second

// catalogLookupTimeout caps the path-resolution query during M3U8 build.
// Smaller because this is on the hot path of generateM3U8ForPlaylist and
// failing fast lets the existing filesystem fallback kick in.
const catalogLookupTimeout = 5 * time.Second

// mirrorWatchlistToCatalog updates the SQLite catalog to reflect the
// current playlist contents:
//   - UpsertTrackStub for each track_id so the FK on watchlist_tracks
//     is always satisfiable, even before the metadata is fully fetched.
//   - SetWatchlistTracks replaces the junction atomically and returns
//     the diff vs the previous state.
//   - If the diff is non-empty (or this is the first sync), capture a
//     PlaylistSnapshot with the frozen track list at this moment.
//
// All operations are best-effort. Failures are logged and the function
// returns silently — the user-facing watchlist row in BoltDB is the
// source of truth, the catalog is the long-term audit trail.
func (w *Watcher) mirrorWatchlistToCatalog(pl *WatchedPlaylist) {
	if w.jm == nil || w.jm.catalog == nil || pl == nil {
		return
	}
	catalog := w.jm.catalog

	ctx, cancel := context.WithTimeout(context.Background(), catalogMirrorTimeout)
	defer cancel()

	// Stub every track first; SetWatchlistTracks insert would fail FK
	// otherwise. Stubs are no-ops once the real metadata exists.
	for _, id := range pl.TrackIDs {
		if id == "" {
			continue
		}
		if err := db.UpsertTrackStub(ctx, catalog, id); err != nil {
			slog.Warn("[Catalog] UpsertTrackStub failed", "spotify_id", id, "err", err)
			return
		}
	}

	added, removed, err := db.SetWatchlistTracks(ctx, catalog, pl.ID, pl.TrackIDs)
	if err != nil {
		slog.Warn("[Catalog] SetWatchlistTracks failed", "playlist", pl.Name, "err", err)
		return
	}

	// Decide whether to capture a snapshot:
	//   - First sync (no prior snapshot): always.
	//   - Otherwise only when the diff is non-empty, to avoid creating
	//     thousands of identical rows for stable playlists.
	latestID, _ := db.LatestSnapshotID(ctx, catalog, pl.ID)
	if latestID != "" && len(added) == 0 && len(removed) == 0 {
		return
	}

	snap := &db.PlaylistSnapshot{
		WatchlistID:  pl.ID,
		PlaylistName: pl.Name,
		AddedCount:   len(added),
		RemovedCount: len(removed),
	}
	if err := db.CreatePlaylistSnapshot(ctx, catalog, snap, pl.TrackIDs); err != nil {
		slog.Warn("[Catalog] CreatePlaylistSnapshot failed", "playlist", pl.Name, "err", err)
	}
}

// catalogPathsForWatchlist returns spotify_id -> file_path for tracks of
// the watchlist that are present in the catalog with status='present'.
// Used as the first source by resolveTrackPaths.
//
// Best-effort: returns an empty map if the catalog is unset, the
// watchlist is empty, or any query/scan error occurs. Callers fall back
// to the existing filesystem index + BoltDB resolution.
func (w *Watcher) catalogPathsForWatchlist(pl *WatchedPlaylist) map[string]string {
	if w.jm == nil || w.jm.catalog == nil || pl == nil {
		return map[string]string{}
	}
	if len(pl.TrackIDs) == 0 {
		return map[string]string{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), catalogLookupTimeout)
	defer cancel()

	placeholders := make([]string, len(pl.TrackIDs))
	args := make([]interface{}, len(pl.TrackIDs))
	for i, id := range pl.TrackIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	// status='present' guarantees the catalog has not flagged the file
	// as missing/moved/corrupt/deleted. The partial unique index ensures
	// at most one row per spotify_id in this set.
	query := `
		SELECT spotify_id, file_path
		FROM library_files
		WHERE status = '` + db.StatusPresent + `'
		  AND spotify_id IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := w.jm.catalog.QueryContext(ctx, query, args...)
	if err != nil {
		slog.Warn("[Catalog] resolveTrackPaths query failed", "playlist", pl.Name, "err", err)
		return map[string]string{}
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			slog.Warn("[Catalog] scan failed", "playlist", pl.Name, "err", err)
			continue
		}
		out[id] = path
	}
	return out
}

// catalogFileSizesForWatchlist returns spotify_id -> file_size (bytes) for
// tracks of the watchlist that are present in the catalog with
// status='present'. Used by GetWatchlistStats so total_size_mb reflects
// the durable catalog instead of only the ephemeral BoltDB job history —
// CleanupOldJobs prunes job rows every 24h, and a track downloaded before
// job-tracking existed (or whose job was pruned) previously contributed
// nothing to the size total even though the file is really on disk.
//
// Best-effort: returns an empty map if the catalog is unset, the
// watchlist is empty, or any query/scan error occurs.
func (w *Watcher) catalogFileSizesForWatchlist(pl *WatchedPlaylist) map[string]int64 {
	if w.jm == nil || w.jm.catalog == nil || pl == nil {
		return map[string]int64{}
	}
	if len(pl.TrackIDs) == 0 {
		return map[string]int64{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), catalogLookupTimeout)
	defer cancel()

	placeholders := make([]string, len(pl.TrackIDs))
	args := make([]interface{}, len(pl.TrackIDs))
	for i, id := range pl.TrackIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `
		SELECT spotify_id, file_size
		FROM library_files
		WHERE status = '` + db.StatusPresent + `'
		  AND spotify_id IN (` + strings.Join(placeholders, ",") + `)`

	rows, err := w.jm.catalog.QueryContext(ctx, query, args...)
	if err != nil {
		slog.Warn("[Catalog] GetWatchlistStats query failed", "playlist", pl.Name, "err", err)
		return map[string]int64{}
	}
	defer rows.Close()

	out := make(map[string]int64)
	for rows.Next() {
		var id string
		var size int64
		if err := rows.Scan(&id, &size); err != nil {
			slog.Warn("[Catalog] GetWatchlistStats scan failed", "playlist", pl.Name, "err", err)
			continue
		}
		out[id] = size
	}
	return out
}
