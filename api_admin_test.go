package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
)

// TestIngestLibraryFileDoesNotRegressValidPath is the regression test for
// the library-rebuild bug: a filesystem walk visiting a stale duplicate
// copy of a track before (or instead of) the real current file must not
// overwrite a still-valid catalog path with the stale one.
func TestIngestLibraryFileDoesNotRegressValidPath(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}

	currentPath := filepath.Join(t.TempDir(), "current.flac")
	if err := os.WriteFile(currentPath, []byte("current"), 0644); err != nil {
		t.Fatalf("write current file: %v", err)
	}

	if err := db.UpsertTrackStub(ctx, database, "spotify:track:1"); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: "spotify:track:1",
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  currentPath,
	}
	if err := db.CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	// Simulate the walk reaching a stale orphan copy of the SAME track at a
	// DIFFERENT path, while the current path is still valid on disk.
	stalePath := filepath.Join(t.TempDir(), "stale-orphan.flac")
	bucket, err := s.ingestLibraryFile(ctx, "spotify:track:1", meta.FullTrackTags{}, stalePath)
	if err != nil {
		t.Fatalf("ingestLibraryFile: %v", err)
	}
	if bucket != ingestDuplicate {
		t.Errorf("bucket = %v, want ingestDuplicate", bucket)
	}

	got, err := db.GetActiveLibraryFile(ctx, database, "spotify:track:1")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if got.FilePath != currentPath {
		t.Errorf("catalog path regressed to %q, want it to stay at %q — library-rebuild would break M3U8 resolution for this track", got.FilePath, currentPath)
	}
}

// TestIngestLibraryFileUpdatesPathWhenOldOneIsGone confirms the legitimate
// "file was actually moved" case still works: if the recorded path no
// longer exists, the new path found by the walk IS the real move target.
func TestIngestLibraryFileUpdatesPathWhenOldOneIsGone(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}

	oldPath := filepath.Join(t.TempDir(), "old.flac") // never created — simulates a genuine move
	if err := db.UpsertTrackStub(ctx, database, "spotify:track:2"); err != nil {
		t.Fatalf("UpsertTrackStub: %v", err)
	}
	lf := &db.LibraryFile{
		SpotifyID: "spotify:track:2",
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  oldPath,
	}
	if err := db.CreateLibraryFile(ctx, database, lf); err != nil {
		t.Fatalf("CreateLibraryFile: %v", err)
	}

	newPath := filepath.Join(t.TempDir(), "new-location.flac")
	bucket, err := s.ingestLibraryFile(ctx, "spotify:track:2", meta.FullTrackTags{}, newPath)
	if err != nil {
		t.Fatalf("ingestLibraryFile: %v", err)
	}
	if bucket != ingestMoved {
		t.Errorf("bucket = %v, want ingestMoved", bucket)
	}

	got, err := db.GetActiveLibraryFile(ctx, database, "spotify:track:2")
	if err != nil {
		t.Fatalf("GetActiveLibraryFile: %v", err)
	}
	if got.FilePath != newPath {
		t.Errorf("catalog path = %q, want the genuinely-moved-to path %q", got.FilePath, newPath)
	}
}

// TestIngestLibraryFileBackfillsTrackMetadata is the regression test for
// the "maintenance function" gap: ingestLibraryFile used to call
// UpsertTrackStub (spotify_id only), so a file rediscovered by
// library-rebuild/repair never got its tracks row enriched even when the
// scan had already read title/artist/isrc/genre/etc — those tags were
// read once (for identification) and then thrown away. It must now do a
// full UpsertTrack with everything the caller already read.
func TestIngestLibraryFileBackfillsTrackMetadata(t *testing.T) {
	ctx := context.Background()
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}

	path := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(path, []byte("fake flac bytes"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	tags := meta.FullTrackTags{
		SpotifyID:   "spotify:track:3",
		Title:       "Some Track",
		Artist:      "Some Artist",
		Album:       "Some Album",
		AlbumArtist: "Some Album Artist",
		ReleaseDate: "2024-03-15",
		ISRC:        "USRC17607839",
		Genre:       "Synthwave",
		Copyright:   "2024 Some Label",
	}
	if _, err := s.ingestLibraryFile(ctx, tags.SpotifyID, tags, path); err != nil {
		t.Fatalf("ingestLibraryFile: %v", err)
	}

	got, err := db.GetTrack(ctx, database, "spotify:track:3")
	if err != nil {
		t.Fatalf("GetTrack: %v", err)
	}
	if got == nil {
		t.Fatal("GetTrack returned nil — ingestLibraryFile left the track as a bare, FK-only stub")
	}
	if got.Name != tags.Title || got.ArtistName != tags.Artist || got.ISRC != tags.ISRC ||
		got.Genre != tags.Genre || got.AlbumName != tags.Album || got.Copyright != tags.Copyright {
		t.Errorf("track metadata not backfilled: got %+v, want it to reflect %+v", got, tags)
	}
}
