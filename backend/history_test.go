package backend

import (
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// newTestHistoryDB wires the package-level history DB singleton to a fresh
// temp BoltDB for the duration of the test, and tears it back down after —
// getHistoryDB()/AddHistoryItem/etc. all read the shared historyDB var.
func newTestHistoryDB(t *testing.T) {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "history-test.db"), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	if err := InitHistoryDBShared(db); err != nil {
		t.Fatalf("InitHistoryDBShared: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		historyMu.Lock()
		historyDB = nil
		historyShared = false
		historyDisabled = false
		historyMu.Unlock()
	})
}

func TestUpdateHistoryItemPathsForRenameUpdatesMatchingItems(t *testing.T) {
	newTestHistoryDB(t)

	if err := AddHistoryItem(HistoryItem{SpotifyID: "renamed-track", Path: "/music/old.flac"}); err != nil {
		t.Fatalf("AddHistoryItem (target): %v", err)
	}
	if err := AddHistoryItem(HistoryItem{SpotifyID: "other-track", Path: "/music/other.flac"}); err != nil {
		t.Fatalf("AddHistoryItem (other): %v", err)
	}

	updated, err := UpdateHistoryItemPathsForRename("/music/old.flac", "/music/new.flac")
	if err != nil {
		t.Fatalf("UpdateHistoryItemPathsForRename: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}

	items, err := GetHistoryItems("")
	if err != nil {
		t.Fatalf("GetHistoryItems: %v", err)
	}
	var sawRenamed, sawOtherUntouched bool
	for _, it := range items {
		switch it.SpotifyID {
		case "renamed-track":
			if it.Path != "/music/new.flac" {
				t.Errorf("renamed-track Path = %q, want /music/new.flac", it.Path)
			}
			sawRenamed = true
		case "other-track":
			if it.Path != "/music/other.flac" {
				t.Errorf("other-track Path was touched: %q", it.Path)
			}
			sawOtherUntouched = true
		}
	}
	if !sawRenamed || !sawOtherUntouched {
		t.Fatalf("missing expected items: sawRenamed=%v sawOtherUntouched=%v", sawRenamed, sawOtherUntouched)
	}
}

func TestUpdateHistoryItemPathsForRenameNoMatchIsNoop(t *testing.T) {
	newTestHistoryDB(t)

	if err := AddHistoryItem(HistoryItem{SpotifyID: "x", Path: "/music/a.flac"}); err != nil {
		t.Fatalf("AddHistoryItem: %v", err)
	}

	updated, err := UpdateHistoryItemPathsForRename("/music/nonexistent.flac", "/music/new.flac")
	if err != nil {
		t.Fatalf("UpdateHistoryItemPathsForRename: %v", err)
	}
	if updated != 0 {
		t.Fatalf("updated = %d, want 0", updated)
	}
}
