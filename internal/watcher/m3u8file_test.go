package watcher

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func testWatcher(t *testing.T) *Watcher {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "test.db"), 0600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Watcher{db: db}
}

func putWatchlist(t *testing.T, w *Watcher, pl WatchedPlaylist) {
	t.Helper()
	err := w.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketWatchlist)
		if err != nil {
			return err
		}
		data, err := json.Marshal(&pl)
		if err != nil {
			return err
		}
		return b.Put([]byte(pl.ID), data)
	})
	if err != nil {
		t.Fatalf("seed watchlist: %v", err)
	}
}

func readWatchlist(t *testing.T, w *Watcher, id string) WatchedPlaylist {
	t.Helper()
	var pl WatchedPlaylist
	err := w.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketWatchlist).Get([]byte(id))
		if raw == nil {
			t.Fatalf("watchlist %s vanished", id)
		}
		return json.Unmarshal(raw, &pl)
	})
	if err != nil {
		t.Fatalf("read watchlist: %v", err)
	}
	return pl
}

// TestSetM3U8FilePreservesConcurrentChanges is the reason setM3U8File does a
// read-modify-write instead of saveWatchlist.
//
// GenerateM3U8ForPlaylist holds a copy of the watchlist it loaded earlier and
// runs from four places, two of which can overlap a sync. If it wrote that copy
// back wholesale, everything the sync had just updated in between — TrackIDs,
// LastSync — would be silently reverted. Here the record changes underneath and
// the update must keep those changes while still recording the filename.
func TestSetM3U8FilePreservesConcurrentChanges(t *testing.T) {
	w := testWatcher(t)
	putWatchlist(t, w, WatchedPlaylist{ID: "watch-1", Name: "Release Radar", TrackIDs: []string{"a"}})

	// A sync lands between the copy being read and the filename being recorded.
	synced := time.Now().UTC().Truncate(time.Second)
	putWatchlist(t, w, WatchedPlaylist{
		ID: "watch-1", Name: "Release Radar",
		TrackIDs: []string{"a", "b", "c"}, LastSync: synced,
	})

	if err := w.setM3U8File("watch-1", "Release Radar [830f8305].m3u8"); err != nil {
		t.Fatalf("setM3U8File: %v", err)
	}

	got := readWatchlist(t, w, "watch-1")
	if got.M3U8File != "Release Radar [830f8305].m3u8" {
		t.Errorf("M3U8File = %q, want it recorded", got.M3U8File)
	}
	if len(got.TrackIDs) != 3 {
		t.Errorf("TrackIDs = %v, want the 3 the concurrent sync wrote — the update clobbered them", got.TrackIDs)
	}
	if !got.LastSync.Equal(synced) {
		t.Errorf("LastSync = %v, want %v — the update reverted it", got.LastSync, synced)
	}
}

// TestSetM3U8FileDoesNotResurrect: a watchlist removed between the read and this
// call must stay removed. saveWatchlist's caller guards the same hazard.
func TestSetM3U8FileDoesNotResurrect(t *testing.T) {
	w := testWatcher(t)
	putWatchlist(t, w, WatchedPlaylist{ID: "watch-gone", Name: "Gone"})
	if err := w.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketWatchlist).Delete([]byte("watch-gone"))
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if err := w.setM3U8File("watch-gone", "Gone.m3u8"); err != nil {
		t.Fatalf("setM3U8File on a removed watchlist should be a no-op, got %v", err)
	}

	err := w.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket(bucketWatchlist).Get([]byte("watch-gone")); raw != nil {
			t.Error("setM3U8File resurrected a removed watchlist")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestSetM3U8FileMissingBucket: the very first call can land before any
// watchlist has ever been saved, so the bucket may not exist yet.
func TestSetM3U8FileMissingBucket(t *testing.T) {
	if err := testWatcher(t).setM3U8File("watch-1", "x.m3u8"); err != nil {
		t.Fatalf("setM3U8File with no bucket should be a no-op, got %v", err)
	}
}
