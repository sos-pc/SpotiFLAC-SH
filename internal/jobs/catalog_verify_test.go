package jobs

import (
	"os"
	"path/filepath"
	"testing"
)

// The one distinction the verification pass rests on: "the file is not there"
// and "I could not tell" must not be the same answer. util.FileExists returns a
// bare bool and folds a permission error into "absent"; used here, an unreadable
// mount would be recorded as a library that lost every file — in one pass, with
// apply=true, on a schedule.
//
// Moved here with statLibraryFile when VerifyLibraryStatuses was extracted from
// the HTTP handler so the daily loop and the endpoint could share one
// implementation.
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

// TestVerifyLibraryStatusesNeedsACatalog: the loop calls this every 24h whether
// or not the catalog was opened. A nil handle must be an error, not a panic in a
// background goroutine.
func TestVerifyLibraryStatusesNeedsACatalog(t *testing.T) {
	if _, err := VerifyLibraryStatuses(t.Context(), nil, true, 10); err == nil {
		t.Error("a nil catalog was accepted")
	}
}

// TestLibraryVerifyResultChanged pins what the loop logs on. A pass that found
// nothing must stay quiet, or a daily line saying "0, 0" trains the reader to
// skip the ones that matter.
func TestLibraryVerifyResultChanged(t *testing.T) {
	cases := []struct {
		name string
		r    LibraryVerifyResult
		want bool
	}{
		{"rien n'a bougé", LibraryVerifyResult{Checked: 2589, Unchanged: 2589}, false},
		{"un fichier a disparu", LibraryVerifyResult{WentMissing: 1}, true},
		{"un fichier est revenu", LibraryVerifyResult{CameBack: 1}, true},
		{"échec de lecture seul", LibraryVerifyResult{Failed: 3}, false},
	}
	for _, c := range cases {
		if got := c.r.Changed(); got != c.want {
			t.Errorf("%s: Changed() = %v, want %v", c.name, got, c.want)
		}
	}
}
