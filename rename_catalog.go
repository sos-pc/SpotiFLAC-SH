package main

// ─────────────────────────────────────────────────────────────────────────────
// Catalog sync for File Manager renames.
//
// Renaming a file via the API never touched the catalog, so a renamed
// track's library_files.file_path went stale, pointing at a path that no
// longer existed. resolveTrackPaths (watcher.go) falls back to a
// filesystem SPOTIFY_ID-tag scan for stale catalog rows — but the fallback
// scan itself was only triggered when at least one track's row was
// completely MISSING, not merely stale (see resolveTrackPaths' comment).
// Between that gating bug (fixed separately) and this one, a rename could
// silently and permanently drop a track from the playlist's M3U8. This file
// closes the root cause by keeping the catalog in sync at rename time.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"fmt"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

// syncCatalogPathOnRename mirrors a filesystem rename into the catalog.
// Reads the SPOTIFY_ID tag from newPath (renaming doesn't touch tag
// content) and, if the catalog's active row for that track currently
// points at oldPath, updates it to newPath.
//
// Best-effort: errors are logged, never returned — a rename the user
// explicitly asked for should never fail because of a catalog sync issue,
// and resolveTrackPaths still falls back to a filesystem tag scan for any
// track whose catalog entry goes stale for some other reason.
func syncCatalogPathOnRename(ctr *Container, oldPath, newPath string) {
	if ctr == nil || ctr.Catalog == nil || oldPath == "" || newPath == "" || oldPath == newPath {
		return
	}
	spotifyID, err := meta.ReadSpotifyID(newPath)
	if err != nil || spotifyID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), catalogWriteTimeout)
	defer cancel()

	existing, err := db.GetActiveLibraryFile(ctx, ctr.Catalog, spotifyID)
	if err != nil || existing == nil || existing.FilePath != oldPath {
		return
	}
	if err := db.UpdateLibraryFilePath(ctx, ctr.Catalog, existing.ID, newPath); err != nil {
		fmt.Printf("[Catalog] rename sync failed for %s: %v\n", spotifyID, err)
	}
}
