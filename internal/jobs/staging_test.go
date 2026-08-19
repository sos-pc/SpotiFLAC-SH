package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkJobDir creates a staging job directory holding one file, with both the file
// and the directory aged by `age`.
func mkJobDir(t *testing.T, root, name string, age time.Duration, size int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(file, make([]byte, size), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(file, when, when); err != nil {
		t.Fatalf("Chtimes file: %v", err)
	}
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatalf("Chtimes dir: %v", err)
	}
	return dir
}

func TestReclaimStagingRemovesOnlyAbandonedJobDirs(t *testing.T) {
	root := t.TempDir()

	// Abandoned: the exact shape left behind on the reference deployment — a
	// finished download whose deferred cleanup never ran.
	old1 := mkJobDir(t, root, "7187b2581f5249449db01c79eb37ce12", 9*24*time.Hour, 1024)
	old2 := mkJobDir(t, root, "d7986e9230f24e8089eab429831e31cb", 9*24*time.Hour, 0)
	// Live: a download in flight right now.
	fresh := mkJobDir(t, root, "aaaabbbbccccddddeeeeffff00001111", time.Minute, 2048)
	// Not ours: same age, wrong name shape.
	//
	// No uppercase-hex case here on purpose: that rule is a property of
	// isJobDirName and is tested there. Creating a directory that differs from
	// another only by case tests the FILESYSTEM, not the rule — on Windows the
	// two are the same directory, so the pair silently collapses into one and
	// the assertions below become meaningless while still passing on Linux.
	notOurs := mkJobDir(t, root, "some-other-thing", 9*24*time.Hour, 512)

	dirs, freed, err := reclaimStaging(root, 6*time.Hour)
	if err != nil {
		t.Fatalf("reclaimStaging: %v", err)
	}
	if dirs != 2 {
		t.Errorf("reclaimed %d directories, want 2", dirs)
	}
	if freed != 1024 {
		t.Errorf("freed %d bytes, want 1024", freed)
	}

	for _, gone := range []string{old1, old2} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived and should not have", filepath.Base(gone))
		}
	}
	for _, kept := range []string{fresh, notOurs} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s was removed and should not have been: %v", filepath.Base(kept), err)
		}
	}
}

// The hazard this rule exists for: the engine runs in its OWN container and can
// still be writing into a directory this process gave up on. A sweep that read
// the directory's own timestamp would delete a live download, because a
// directory's mtime does not change while a file inside it grows — only when an
// entry is created or removed.
func TestReclaimStagingUsesNewestWriteNotDirectoryMtime(t *testing.T) {
	root := t.TempDir()
	dir := mkJobDir(t, root, "0123456789abcdef0123456789abcdef", 9*24*time.Hour, 4096)

	// The directory still looks nine days old; the file inside was written a
	// moment ago, which is what a long in-flight download looks like.
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, "track.flac"), now, now); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	dirs, _, err := reclaimStaging(root, 6*time.Hour)
	if err != nil {
		t.Fatalf("reclaimStaging: %v", err)
	}
	if dirs != 0 {
		t.Errorf("reclaimed %d directories; a live download was deleted", dirs)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the in-flight job directory was removed: %v", err)
	}
}

func TestReclaimStagingMissingRootIsNotAnError(t *testing.T) {
	dirs, freed, err := reclaimStaging(filepath.Join(t.TempDir(), "never-created"), time.Hour)
	if err != nil {
		t.Fatalf("a staging volume with nothing staged yet must not be an error: %v", err)
	}
	if dirs != 0 || freed != 0 {
		t.Errorf("reclaimed %d dirs / %d bytes from a missing root", dirs, freed)
	}
}

func TestIsJobDirName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"7187b2581f5249449db01c79eb37ce12", true},
		{"7187B2581F5249449DB01C79EB37CE12", false},  // uppercase: not what uuid4().hex produces
		{"7187b2581f5249449db01c79eb37ce1", false},   // 31 chars
		{"7187b2581f5249449db01c79eb37ce123", false}, // 33 chars
		{"7187b2581f5249449db01c79eb37cezz", false},  // not hex
		{"Playlists", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isJobDirName(tt.name); got != tt.want {
			t.Errorf("isJobDirName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
