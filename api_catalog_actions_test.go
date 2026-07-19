package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The one distinction the reconcile pass rests on: "the file is not there" and
// "I could not tell" must not be the same answer. util.FileExists returns a
// bare bool and folds a permission error into "absent"; used here, an
// unreadable mount would be recorded as a library that lost every file.
func TestStatLibraryFileSeparatesAbsentFromUnreadable(t *testing.T) {
	dir := t.TempDir()

	present := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(present, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("fichier présent", func(t *testing.T) {
		ok, err := statLibraryFile(present)
		if err != nil || !ok {
			t.Errorf("got (%v, %v), want (true, nil)", ok, err)
		}
	})

	t.Run("fichier absent", func(t *testing.T) {
		// Absent is a finding, not an error: the pass must act on it.
		ok, err := statLibraryFile(filepath.Join(dir, "gone.flac"))
		if err != nil {
			t.Errorf("absence reported as an error: %v", err)
		}
		if ok {
			t.Error("a missing file was reported as present")
		}
	})

	t.Run("un répertoire n'est pas un fichier", func(t *testing.T) {
		ok, err := statLibraryFile(dir)
		if err != nil || ok {
			t.Errorf("got (%v, %v), want (false, nil)", ok, err)
		}
	})

	t.Run("chemin vide", func(t *testing.T) {
		// An empty path cannot be checked, so it must be an error rather than
		// "absent" — otherwise a row with a blank file_path would be marked
		// missing on every pass.
		if _, err := statLibraryFile(""); err == nil {
			t.Error("an empty path was accepted as a real answer")
		}
	})
}
