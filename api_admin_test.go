package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/db"
	"github.com/afkarxyz/SpotiFLAC/backend/meta"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// writeTestFlacWithSpotifyID mirrors writeTestFlacWithTags (jobs_catalog_test.go)
// but embeds SPOTIFY_ID instead of ISRC/GENRE — what scanRootForRebuild
// actually keys its walk on (files with no SPOTIFY_ID tag are skipped as
// NoTag, never reaching ingestLibraryFile at all).
func writeTestFlacWithSpotifyID(t *testing.T, path, spotifyID string) {
	t.Helper()
	cmt := flacvorbis.New()
	if err := cmt.Add(meta.SpotifyIDTagKey, spotifyID); err != nil {
		t.Fatalf("add SPOTIFY_ID comment: %v", err)
	}
	block := cmt.Marshal()
	f := &flac.File{Meta: []*flac.MetaDataBlock{&block}, Frames: []byte{0xFF, 0xF8}}
	if err := os.WriteFile(path, f.Marshal(), 0644); err != nil {
		t.Fatalf("write test FLAC: %v", err)
	}
}

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

// TestIngestLibraryFileFailsFastOnExpiredContext is the regression test for
// the "one shared clock for the whole scan" bug: library-rebuild used to
// pass every file the SAME context, sharing one deadline across the entire
// walk, so a large-but-healthy library could have its very last file cut
// off by "context deadline exceeded" purely because earlier files had
// already spent the shared budget — observed in practice on a clean
// 1800-file scan. Each file now gets its own short-lived context
// (scanRootForRebuild derives a fresh one via
// context.WithTimeout(ctx, libraryRebuildPerFileTimeout) per file). This
// test verifies the two halves of that contract: an already-expired
// context fails the file it's used for, and — critically — does not affect
// a subsequent call made with a fresh context for a different file.
func TestIngestLibraryFileFailsFastOnExpiredContext(t *testing.T) {
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}

	expiredCtx, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expiredCtx.Done() // guarantee it's actually expired, not a timing race

	badPath := filepath.Join(t.TempDir(), "stuck.flac")
	if _, err := s.ingestLibraryFile(expiredCtx, "spotify:track:stuck", meta.FullTrackTags{SpotifyID: "spotify:track:stuck"}, badPath); err == nil {
		t.Error("ingestLibraryFile with an expired context should fail for that file, not silently succeed")
	}

	// The next file, with its own fresh context, must succeed normally —
	// the previous file's expired context must not have left the shared
	// catalog connection or any other state in a way that poisons this call.
	goodPath := filepath.Join(t.TempDir(), "fine.flac")
	if err := os.WriteFile(goodPath, []byte("fake flac bytes"), 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	freshCtx, cancel2 := context.WithTimeout(context.Background(), libraryRebuildPerFileTimeout)
	defer cancel2()
	if _, err := s.ingestLibraryFile(freshCtx, "spotify:track:fine", meta.FullTrackTags{SpotifyID: "spotify:track:fine"}, goodPath); err != nil {
		t.Errorf("ingestLibraryFile for a different file with a fresh context should succeed, got: %v", err)
	}
}

// TestScanRootForRebuildImportsMultipleFiles is a sanity check for the
// scanRootForRebuild refactor (each file now gets its own per-file context
// instead of sharing the caller's): a normal multi-file walk must still
// import every tagged file, unaffected by the extra context.WithTimeout
// wrapping added around each ingestLibraryFile call.
func TestScanRootForRebuildImportsMultipleFiles(t *testing.T) {
	database := openTestCatalogDB(t)
	s := &Server{ctr: &Container{Catalog: database}}

	root := t.TempDir()
	for i, id := range []string{"spotify:track:a", "spotify:track:b", "spotify:track:c"} {
		path := filepath.Join(root, fmt.Sprintf("track-%d.flac", i))
		writeTestFlacWithSpotifyID(t, path, id)
	}

	ctx, cancel := context.WithTimeout(context.Background(), libraryRebuildTimeout)
	defer cancel()
	result := &libraryRebuildResult{}
	s.scanRootForRebuild(ctx, root, result, make(map[string]bool))

	if result.FilesScanned != 3 || result.Imported != 3 || result.Failed != 0 {
		t.Errorf("scanRootForRebuild = %+v, want 3 scanned/imported, 0 failed", result)
	}
}

// TestLibraryRebuildAsyncPublishesSSEEvent is the regression test for a
// production bug: v1LibraryRebuild used to run the filesystem walk
// synchronously inside the request handler, deriving its context from
// r.Context() — a walk that can take many minutes on a real library
// easily outlives a reverse-proxy's read timeout, and the moment the
// client/proxy gave up on the connection, the cancelled r.Context()
// killed the in-flight scan (observed live in production: "upsert track:
// ... context canceled" after 1200+ files had already imported cleanly).
// runLibraryRebuildAsync now runs against context.Background() instead,
// so a client disconnect can no longer abort the scan; completion is
// announced over SSE instead of in the (now long-gone) HTTP response.
func TestLibraryRebuildAsyncPublishesSSEEvent(t *testing.T) {
	jm, hub := newTestJobManagerWithHub(t, true)
	s := &Server{ctr: &Container{Catalog: jm.catalog, Jobs: jm, SSE: hub}}

	root := t.TempDir()
	writeTestFlacWithSpotifyID(t, filepath.Join(root, "track.flac"), "spotify:track:a")

	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	s.runLibraryRebuildAsync([]string{root})

	select {
	case ev := <-sub:
		if ev.Type != "library_rebuild_done" {
			t.Fatalf("event type = %q, want %q", ev.Type, "library_rebuild_done")
		}
		result, ok := ev.Data.(libraryRebuildResult)
		if !ok {
			t.Fatalf("event.Data type = %T, want libraryRebuildResult", ev.Data)
		}
		if result.FilesScanned != 1 || result.Imported != 1 {
			t.Errorf("result = %+v, want 1 scanned/imported", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for library_rebuild_done SSE event")
	}
}

// TestRetagIncompleteMetadataAsyncPublishesSSEEvent covers the same fix
// applied to retag-incomplete-metadata's sibling maintenance endpoint —
// same failure mode (a slow per-track pass outliving a proxy's read
// timeout), same fix (run against context.Background(), announce
// completion over SSE instead of returning it in the HTTP response).
func TestRetagIncompleteMetadataAsyncPublishesSSEEvent(t *testing.T) {
	jm, hub := newTestJobManagerWithHub(t, true)
	s := &Server{ctr: &Container{Catalog: jm.catalog, Jobs: jm, SSE: hub}}

	sub := hub.subscribe()
	defer hub.unsubscribe(sub)

	// No tracks needing retag — this test only verifies the async+publish
	// plumbing, not the retag logic itself (already covered elsewhere).
	s.runRetagIncompleteMetadataAsync(nil)

	select {
	case ev := <-sub:
		if ev.Type != "retag_incomplete_metadata_done" {
			t.Fatalf("event type = %q, want %q", ev.Type, "retag_incomplete_metadata_done")
		}
		result, ok := ev.Data.(retagIncompleteMetadataResult)
		if !ok {
			t.Fatalf("event.Data type = %T, want retagIncompleteMetadataResult", ev.Data)
		}
		if result.Scanned != 0 {
			t.Errorf("result.Scanned = %d, want 0", result.Scanned)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for retag_incomplete_metadata_done SSE event")
	}
}
