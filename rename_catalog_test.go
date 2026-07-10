package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
)

// These tests exercise syncCatalogPathOnRename's guard clauses and its
// safe-failure behavior on a file with no readable SPOTIFY_ID tag. The full
// happy path (real tagged audio file -> catalog row updated) needs a real
// FLAC/MP3 fixture to exercise meta.ReadSpotifyID end-to-end, which this
// package doesn't have test fixtures for; that path was verified by code
// review instead (ReadSpotifyID is the same function library-rebuild
// already relies on in api_admin_test.go's ingest tests, just called with a
// literal path instead of via a filesystem walk).

func TestSyncCatalogPathOnRenameNoOpsSafely(t *testing.T) {
	database := openTestCatalogDB(t)
	ctr := &Container{Catalog: database}

	t.Run("nil container", func(t *testing.T) {
		syncCatalogPathOnRename(nil, "/a", "/b") // must not panic
	})
	t.Run("nil catalog", func(t *testing.T) {
		syncCatalogPathOnRename(&Container{}, "/a", "/b") // must not panic
	})
	t.Run("same path", func(t *testing.T) {
		syncCatalogPathOnRename(ctr, "/a", "/a") // no-op, must not touch the DB
	})
	t.Run("empty paths", func(t *testing.T) {
		syncCatalogPathOnRename(ctr, "", "/b")
		syncCatalogPathOnRename(ctr, "/a", "")
	})
}

func TestSyncCatalogPathOnRenameSkipsUntaggedFile(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)
	ctr := &Container{Catalog: database}

	oldPath := filepath.Join(t.TempDir(), "old.flac")
	newPath := filepath.Join(t.TempDir(), "new.flac")
	if err := os.WriteFile(newPath, []byte("not a real audio file"), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	if err := db.UpsertTrackStub(ctx, database, "spotify:track:rename-test"); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: "spotify:track:rename-test",
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  oldPath,
	}
	if err := db.CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	// newPath isn't a parseable audio file, so ReadSpotifyID fails and this
	// must be a safe no-op — the catalog row must stay exactly as it was.
	syncCatalogPathOnRename(ctr, oldPath, newPath)

	got, err := db.GetActiveLibraryFile(ctx, database, "spotify:track:rename-test")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if got.FilePath != oldPath {
		t.Errorf("FilePath = %q, want unchanged %q (rename sync should have no-opped on an unparseable file)", got.FilePath, oldPath)
	}
}
