package isrclookup

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

const isrcCacheBucket = "SpotifyTrackISRC"

type isrcCacheEntry struct {
	ISRC      string `json:"isrc"`
	UpdatedAt int64  `json:"updated_at"`
}

var (
	isrcCacheDB *bolt.DB
	isrcCacheMu sync.Mutex
)

// InitCacheDB registers the ISRC cache bucket in the app's shared
// BoltDB (same file as jobs/watchlists/users/history) instead of opening a
// separate database file. Safe to skip: every reader/writer below treats an
// uninitialized cache as a no-op rather than failing the lookup.
func InitCacheDB(db *bolt.DB) error {
	isrcCacheMu.Lock()
	defer isrcCacheMu.Unlock()

	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(isrcCacheBucket))
		return err
	}); err != nil {
		return fmt.Errorf("isrc cache bucket init failed: %w", err)
	}

	isrcCacheDB = db
	return nil
}

// GetCachedISRC returns the cached ISRC for a Spotify track ID, or "" if
// absent or the cache isn't initialized.
func GetCachedISRC(trackID string) (string, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" || isrcCacheDB == nil {
		return "", nil
	}

	var cached string
	err := isrcCacheDB.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(isrcCacheBucket))
		if bucket == nil {
			return nil
		}
		value := bucket.Get([]byte(trackID))
		if len(value) == 0 {
			return nil
		}
		var entry isrcCacheEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return err
		}
		cached = strings.ToUpper(strings.TrimSpace(entry.ISRC))
		return nil
	})
	return cached, err
}

// PutCachedISRC stores a resolved ISRC for a Spotify track ID. A no-op if
// the cache isn't initialized, so callers don't need to guard every call.
func PutCachedISRC(trackID, isrc string) error {
	trackID = strings.TrimSpace(trackID)
	isrc = strings.ToUpper(strings.TrimSpace(isrc))
	if trackID == "" || isrc == "" || isrcCacheDB == nil {
		return nil
	}

	payload, err := json.Marshal(isrcCacheEntry{ISRC: isrc, UpdatedAt: time.Now().Unix()})
	if err != nil {
		return err
	}

	return isrcCacheDB.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(isrcCacheBucket))
		if err != nil {
			return err
		}
		return bucket.Put([]byte(trackID), payload)
	})
}
