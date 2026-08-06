package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRenameFileToSanitizesTraversalInNewName(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.flac")
	if err := os.WriteFile(oldPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	f := NewFileService(nil, nil)
	if err := f.RenameFileTo(oldPath, "../../../etc/cron.d/evil"); err != nil {
		t.Fatalf("RenameFileTo: %v", err)
	}

	// A traversal-laden newName must be sanitized down to a bare filename
	// and stay inside dir — it must never escape to a sibling/ancestor
	// directory (N2).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one file left in %s, got %v", dir, entries)
	}
	renamed := filepath.Join(dir, entries[0].Name())
	if filepath.Dir(renamed) != dir {
		t.Errorf("renamed file escaped dir: %s", renamed)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "..", "..", "etc", "cron.d", "evil")); err == nil {
		t.Error("traversal path was actually created outside dir")
	}
}

func TestRenameFileToRefusesToOverwriteExistingFile(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.flac")
	targetPath := filepath.Join(dir, "target.flac")
	if err := os.WriteFile(oldPath, []byte("new content"), 0644); err != nil {
		t.Fatalf("setup old: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("original content — must survive"), 0644); err != nil {
		t.Fatalf("setup target: %v", err)
	}

	f := NewFileService(nil, nil)
	err := f.RenameFileTo(oldPath, "target")
	if err == nil {
		t.Fatal("expected a collision error, got nil")
	}

	// The pre-existing file must be untouched — os.Rename would otherwise
	// have silently replaced it with no warning.
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("target.flac disappeared: %v", readErr)
	}
	if string(got) != "original content — must survive" {
		t.Errorf("target.flac was overwritten: got %q", got)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("old.flac should still exist since the rename was refused: %v", err)
	}
}
