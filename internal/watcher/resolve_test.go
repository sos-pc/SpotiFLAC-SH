package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveTrackPathsReportsWhatItCouldNotPlace replaces
// TestNeedsFilesystemIndexFallback, whose subject no longer exists: generation
// used to walk the library reading SPOTIFY_ID tags whenever any track was
// unresolved, and that function decided when.
//
// What matters now is the other half of the contract. Unresolved IDs used to be
// dropped on the floor and inferred afterwards from a length difference; they
// are returned, so the caller can say which tracks rather than how many.
func TestResolveTrackPathsReportsWhatItCouldNotPlace(t *testing.T) {
	dir := t.TempDir()
	// No catalog and no job manager: nothing can resolve, which is the shape
	// that makes the reporting observable without a database.
	w := &Watcher{}

	pl := &WatchedPlaylist{ID: "watch-1", Name: "Jazz", TrackIDs: []string{"a", "b", "c"}}
	paths, unresolved := w.resolveTrackPaths(pl, dir)

	if len(paths) != 0 {
		t.Errorf("paths = %v, want none — nothing could resolve", paths)
	}
	if len(unresolved) != 3 {
		t.Fatalf("unresolved = %v, want all three IDs", unresolved)
	}
	for i, want := range []string{"a", "b", "c"} {
		if unresolved[i] != want {
			t.Errorf("unresolved[%d] = %q, want %q — order follows the playlist", i, unresolved[i], want)
		}
	}
}

// TestResolveTrackPathsDoesNotWalkTheLibrary is the point of the change, stated
// as a test: a track that resolves to nothing must not cause the library to be
// read. The walk cost ~15 s cold over 2744 files and fired on every generation
// because one never-downloaded track is unresolved forever.
//
// A file carrying the tag is placed in the tree and never referenced by the
// catalog or by a job. Before this change the walk found it; now nothing does,
// and the ID is reported instead.
func TestResolveTrackPathsDoesNotWalkTheLibrary(t *testing.T) {
	dir := t.TempDir()
	stray := filepath.Join(dir, "Artist", "Album")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stray, "track.flac"), []byte("fLaC"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	w := &Watcher{}
	pl := &WatchedPlaylist{ID: "watch-1", Name: "Jazz", TrackIDs: []string{"tagged-track"}}
	paths, unresolved := w.resolveTrackPaths(pl, dir)

	if len(paths) != 0 {
		t.Errorf("paths = %v — the library was scanned; that walk is what this removed", paths)
	}
	if len(unresolved) != 1 || unresolved[0] != "tagged-track" {
		t.Errorf("unresolved = %v, want the one ID reported for repair", unresolved)
	}
}

// TestLegacyJobPathsWithoutAJobManager: resolveTrackPaths runs from a startup
// hook and from freshness checks, and the fallback it consults dereferenced the
// job manager without a guard while its neighbour recoverMissingFiles had one.
func TestLegacyJobPathsWithoutAJobManager(t *testing.T) {
	w := &Watcher{}
	if got := w.legacyJobPaths("watch-1"); len(got) != 0 {
		t.Errorf("got %v, want an empty map rather than a panic", got)
	}
}
