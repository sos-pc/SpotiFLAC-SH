package main

// ─────────────────────────────────────────────────────────────────────────────
// Cross-store sync for File Manager renames.
//
// A filesystem rename by itself only affects the file on disk — three other
// stores independently record that file's path and go stale unless told
// about the rename explicitly:
//   - the SQLite catalog (library_files.file_path)
//   - BoltDB jobs (Job.FilePath), read directly by watcher.go's
//     recoverMissingFiles (would otherwise think the file vanished and
//     redundantly re-download it) and its playlist-track-removal path
//     (os.Remove on the stale path silently fails, leaking the renamed
//     file on disk forever)
//   - download history (HistoryItem.Path), used for the "Open File" action
//
// resolveTrackPaths (watcher.go) falls back to a filesystem SPOTIFY_ID-tag
// scan for a stale/missing catalog row, so M3U8 generation has a safety
// net — but the other two stores don't, which is what this file closes.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"database/sql"
	"log/slog"

	"github.com/afkarxyz/SpotiFLAC/backend"
	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

// syncCatalogPathOnRename mirrors a filesystem rename into every store that
// independently records the file's path. Each store is synced
// independently of the others — the catalog lookup is keyed by the
// SPOTIFY_ID tag (renaming doesn't touch tag content) and so can't find a
// row if the tag is missing/unreadable, but the BoltDB job and history
// updates key directly off oldPath and don't depend on it.
//
// Best-effort throughout: errors are logged, never returned — a rename the
// user explicitly asked for should never fail because a downstream sync
// issue, and resolveTrackPaths still falls back to a filesystem tag scan
// for any track whose catalog entry goes stale for some other reason.
// Takes the two stores it touches rather than the whole Container: holding
// *Container forced FileService to hold one too, which made the service layer
// depend on the server layer for a type it never otherwise used.
func syncCatalogPathOnRename(catalog *sql.DB, jobs *JobManager, oldPath, newPath string) {
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return
	}

	if catalog != nil {
		if spotifyID, err := meta.ReadSpotifyID(newPath); err == nil && spotifyID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
			existing, err := db.GetActiveLibraryFile(ctx, catalog, spotifyID)
			if err == nil && existing != nil && existing.FilePath == oldPath {
				if err := db.UpdateLibraryFilePath(ctx, catalog, existing.ID, newPath); err != nil {
					slog.Warn("[Catalog] rename sync failed", "spotify_id", spotifyID, "err", err)
				}
			}
			cancel()
		}
	}

	if jobs != nil {
		if n, err := jobs.UpdateJobFilePathsForRename(oldPath, newPath); err != nil {
			slog.Warn("[Catalog] job FilePath rename sync failed", "old_path", oldPath, "err", err)
		} else if n > 0 {
			slog.Info("[Catalog] updated job(s) FilePath for rename", "count", n, "old_path", oldPath, "new_path", newPath)
		}
	}

	if n, err := backend.UpdateHistoryItemPathsForRename(oldPath, newPath); err != nil {
		slog.Warn("[Catalog] history Path rename sync failed", "old_path", oldPath, "err", err)
	} else if n > 0 {
		slog.Info("[Catalog] updated history item(s) Path for rename", "count", n, "old_path", oldPath, "new_path", newPath)
	}
}
