package meta

import (
	"os"
	"path/filepath"
	"testing"

	flac "github.com/go-flac/go-flac"
)

// TestSaveFlacAtomicWritesExpectedContent confirms the basic write path
// works: saveFlacAtomic must produce a file containing exactly f.Marshal().
func TestSaveFlacAtomicWritesExpectedContent(t *testing.T) {
	f := &flac.File{}
	path := filepath.Join(t.TempDir(), "out.flac")

	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := f.Marshal()
	if string(got) != string(want) {
		t.Errorf("file content = %q, want %q", got, want)
	}
}

// TestSaveFlacAtomicOverwritesFullyAndLeavesNoTempFile is the regression
// test for the non-atomic-write bug: the previous content must be fully
// replaced (not appended to, not left truncated), and no ".tagtmp"
// scratch file should remain after a successful save — go-flac's own
// File.Save truncates the target in place, which is the pattern this
// function exists to avoid.
func TestSaveFlacAtomicOverwritesFullyAndLeavesNoTempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.flac")
	if err := os.WriteFile(path, []byte("this is old, longer content that should be fully replaced"), 0644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	f := &flac.File{}
	if err := saveFlacAtomic(f, path); err != nil {
		t.Fatalf("saveFlacAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := f.Marshal()
	if string(got) != string(want) {
		t.Errorf("file content = %q, want %q (old content leaked through — not a clean overwrite)", got, want)
	}

	if _, err := os.Stat(path + ".tagtmp"); !os.IsNotExist(err) {
		t.Errorf("expected no leftover .tagtmp file, stat returned err=%v", err)
	}
}

// TestSaveFlacAtomicFailsOnUnwritableDirectory confirms a failure to
// create the temp file surfaces as an error rather than silently
// succeeding or panicking.
func TestSaveFlacAtomicFailsOnUnwritableDirectory(t *testing.T) {
	f := &flac.File{}
	badPath := filepath.Join(t.TempDir(), "does", "not", "exist", "out.flac")
	if err := saveFlacAtomic(f, badPath); err == nil {
		t.Error("expected an error writing into a non-existent directory, got nil")
	}
}
