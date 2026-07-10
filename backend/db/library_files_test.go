package db

import (
	"context"
	"database/sql"
	"testing"
)

// openTestCatalog opens a fresh catalog database in a temp dir with
// migrations applied, matching what main.go does at startup.
func openTestCatalog(t *testing.T) *sql.DB {
	t.Helper()
	database, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestUpdateLibraryFileQualityRefreshesInPlace(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalog(t)

	if err := UpsertTrackStub(ctx, database, "spotify:track:abc"); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}

	lf := &LibraryFile{
		SpotifyID: "spotify:track:abc",
		Provider:  "tidal",
		Quality:   QualityLossless,
		Format:    "flac",
		FilePath:  "/music/Artist/Track.flac",
		FileSize:  1000,
	}
	if err := CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	// Simulate a quality-upgrade download landing at the exact same path
	// (the naming template doesn't encode bitrate) — this is the scenario
	// that used to leave the catalog permanently stale.
	if err := UpdateLibraryFileQuality(ctx, database, lf.ID, "tidal", QualityHiResLossless, 5000); err != nil {
		t.Fatalf("UpdateLibraryFileQuality: %v", err)
	}

	got, err := GetActiveLibraryFile(ctx, database, "spotify:track:abc")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if got == nil {
		t.Fatal("expected an active library_file, got nil")
	}
	if got.ID != lf.ID {
		t.Errorf("expected the SAME row to be updated in place (id %q), got a different row (id %q)", lf.ID, got.ID)
	}
	if got.Quality != QualityHiResLossless {
		t.Errorf("Quality = %q, want %q (upgrade was not recorded)", got.Quality, QualityHiResLossless)
	}
	if got.QualityRank != QualityRank(QualityHiResLossless) {
		t.Errorf("QualityRank = %d, want %d", got.QualityRank, QualityRank(QualityHiResLossless))
	}
	if got.FileSize != 5000 {
		t.Errorf("FileSize = %d, want 5000", got.FileSize)
	}
	if got.FilePath != lf.FilePath {
		t.Errorf("FilePath changed unexpectedly: got %q, want %q", got.FilePath, lf.FilePath)
	}
}

func TestUpdateLibraryFileQualityRequiresID(t *testing.T) {
	database := openTestCatalog(t)
	if err := UpdateLibraryFileQuality(context.Background(), database, "", "tidal", QualityLossless, 100); err == nil {
		t.Error("expected an error for empty id, got nil")
	}
}
