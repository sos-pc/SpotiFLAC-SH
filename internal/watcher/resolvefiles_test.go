package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/backend/db"
)

// seedCatalogFile creates a track and a library_files row pointing at path.
func seedCatalogFile(t *testing.T, catalog interface{ Close() error }, spotifyID, path, status string) {
	t.Helper()
	c, ok := catalog.(db.Querier)
	if !ok {
		t.Fatalf("catalog is not a Querier")
	}
	ctx := context.Background()
	if err := db.UpsertTrack(ctx, c, &db.Track{SpotifyID: spotifyID, Name: spotifyID}); err != nil {
		t.Fatalf("UpsertTrack(%s): %v", spotifyID, err)
	}
	lf := &db.LibraryFile{
		SpotifyID: spotifyID,
		Provider:  "tidal",
		Quality:   db.QualityLossless,
		Format:    "flac",
		FilePath:  path,
		FileSize:  1,
		Status:    status,
	}
	if err := db.CreateLibraryFile(ctx, c, lf); err != nil {
		t.Fatalf("CreateLibraryFile(%s): %v", spotifyID, err)
	}
}

// TestResolveTrackFilesNeverReturnsAPathNothingIsAt is the regression test for
// the bug that made deletion silently leak files.
//
// A File Manager rename updated the catalog and left BoltDB's job pointing at
// the old path. Deletion read job.FilePath, called os.Remove on a path nothing
// was at, got ErrNotExist — which the caller treats as "already gone" — and
// reported success while the real file stayed on disk forever
// (internal/jobs/storage.go). UpdateJobFilePathsForRename was written to paper
// over it by writing the new path into every store.
//
// The invariant that removes the need for that: a resolved path always
// stat'd. A row whose file is gone resolves to nothing, so no caller is ever
// handed a path that would make os.Remove lie.
func TestResolveTrackFilesNeverReturnsAPathNothingIsAt(t *testing.T) {
	dir := t.TempDir()
	catalog, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer catalog.Close()

	real := filepath.Join(dir, "here.flac")
	if err := os.WriteFile(real, []byte("fLaC"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	seedCatalogFile(t, catalog, "track-there", real, db.StatusPresent)
	// A row that still says "present" while the file has gone — exactly what a
	// rename leaves behind until the daily verification pass catches up.
	seedCatalogFile(t, catalog, "track-gone", filepath.Join(dir, "moved-away.flac"), db.StatusPresent)

	w := &Watcher{catalog: catalog}
	pl := &WatchedPlaylist{ID: "watch-1", Name: "Jazz",
		TrackIDs: []string{"track-there", "track-gone", "track-unknown"}}

	files := w.resolveTrackFiles(pl)

	if got := files["track-there"]; got != real {
		t.Errorf("track-there = %q, want %q", got, real)
	}
	if got, ok := files["track-gone"]; ok {
		t.Errorf("track-gone resolved to %q — a path nothing is at; os.Remove on it "+
			"returns ErrNotExist and the deletion reports success", got)
	}
	if _, ok := files["track-unknown"]; ok {
		t.Error("a track with no row at all resolved to something")
	}
}

// TestResolveTrackFilesAndPathsAgree: the ordered list generation writes and the
// per-track lookup deletion uses must be two views of one answer, not two
// resolutions that can differ. They differed until this commit — generation read
// catalog-then-jobs, deletion read job.FilePath alone — and a track they
// disagreed about was written into the M3U8 at one path and deleted at another.
func TestResolveTrackFilesAndPathsAgree(t *testing.T) {
	dir := t.TempDir()
	catalog, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer catalog.Close()

	var want []string
	for _, name := range []string{"a", "b"} {
		p := filepath.Join(dir, name+".flac")
		if err := os.WriteFile(p, []byte("fLaC"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		seedCatalogFile(t, catalog, "track-"+name, p, db.StatusPresent)
		want = append(want, p)
	}
	seedCatalogFile(t, catalog, "track-c", filepath.Join(dir, "absent.flac"), db.StatusPresent)

	w := &Watcher{catalog: catalog}
	pl := &WatchedPlaylist{ID: "watch-1", Name: "Jazz",
		TrackIDs: []string{"track-a", "track-b", "track-c"}}

	paths, unresolved := w.resolveTrackPaths(pl, dir)
	files := w.resolveTrackFiles(pl)

	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i, p := range paths {
		if p != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, p, want[i])
		}
		// Every path the playlist file lists must be the one deletion would remove.
		if files[pl.TrackIDs[i]] != p {
			t.Errorf("track %s: generation lists %q, deletion would remove %q",
				pl.TrackIDs[i], p, files[pl.TrackIDs[i]])
		}
	}
	if len(unresolved) != 1 || unresolved[0] != "track-c" {
		t.Errorf("unresolved = %v, want [track-c]", unresolved)
	}
}
