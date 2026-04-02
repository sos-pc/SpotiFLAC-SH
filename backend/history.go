package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

type HistoryItem struct {
	ID          string `json:"id"`
	SpotifyID   string `json:"spotify_id"`
	Title       string `json:"title"`
	Artists     string `json:"artists"`
	Album       string `json:"album"`
	DurationStr string `json:"duration_str"`
	CoverURL    string `json:"cover_url"`
	Quality     string `json:"quality"`
	Format      string `json:"format"`
	Path        string `json:"path"`
	Timestamp   int64  `json:"timestamp"`
	// FIX #4 — isolation par user (vide = item migré, visible par tous)
	UserID      string `json:"user_id,omitempty"`
}

type FetchHistoryItem struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Info      string `json:"info"`
	Image     string `json:"image"`
	Data      string `json:"data"`
	Timestamp int64  `json:"timestamp"`
	// FIX #4 — isolation par user
	UserID    string `json:"user_id,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// State
// ─────────────────────────────────────────────────────────────────────────────

var (
	historyDB        *bolt.DB
	historyDisabled  bool
	historyShared    bool
	historyConfigDir string
	historyMu        sync.Mutex
)

const (
	historyBucket      = "DownloadHistory"
	fetchHistoryBucket = "FetchHistory"
	maxHistory         = 10000
)

// ─────────────────────────────────────────────────────────────────────────────
// Init
// ─────────────────────────────────────────────────────────────────────────────

func InitHistoryDBAt(configDir string) error {
	historyMu.Lock()
	defer historyMu.Unlock()

	historyConfigDir = configDir

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config dir: %w", err)
	}
	dbPath := filepath.Join(configDir, "history.db")

	timeouts := []time.Duration{3 * time.Second, 5 * time.Second, 8 * time.Second}
	var lastErr error

	for attempt, timeout := range timeouts {
		if attempt > 0 {
			fmt.Printf("[History] Retry %d/%d opening history.db (timeout: %v)...\n",
				attempt, len(timeouts)-1, timeout)
			time.Sleep(time.Duration(attempt) * time.Second)
		}

		db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: timeout})
		if err != nil {
			lastErr = err
			fmt.Printf("[History] Attempt %d failed: %v\n", attempt+1, err)
			continue
		}

		err = db.Update(func(tx *bolt.Tx) error {
			if _, err := tx.CreateBucketIfNotExists([]byte(historyBucket)); err != nil {
				return err
			}
			if _, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket)); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			db.Close()
			lastErr = err
			continue
		}

		historyDB = db
		historyDisabled = false
		fmt.Printf("[History] history.db opened: %s\n", dbPath)
		return nil
	}

	historyDisabled = true
	fmt.Printf("[History] WARNING: history DB unavailable after %d attempts: %v\n",
		len(timeouts), lastErr)
	return fmt.Errorf("history DB unavailable: %w", lastErr)
}

func InitHistoryDB(appName string) error {
	appDir, err := util.GetFFmpegDir()
	if err != nil {
		return err
	}
	return InitHistoryDBAt(appDir)
}

func CloseHistoryDB() {
	if historyShared {
		return
	}
	historyMu.Lock()
	defer historyMu.Unlock()
	if historyDB != nil {
		historyDB.Close()
		historyDB = nil
	}
}

func getHistoryDB() (*bolt.DB, error) {
	historyMu.Lock()
	defer historyMu.Unlock()

	if historyDisabled {
		return nil, fmt.Errorf("history DB is disabled (failed to open at startup)")
	}
	if historyDB != nil {
		return historyDB, nil
	}

	if historyConfigDir == "" {
		return nil, fmt.Errorf("history DB not initialized")
	}

	dbPath := filepath.Join(historyConfigDir, "history.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		historyDisabled = true
		return nil, fmt.Errorf("history DB re-init failed: %w", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(historyBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("history DB bucket init failed: %w", err)
	}

	historyDB = db
	fmt.Printf("[History] history.db re-initialized: %s\n", dbPath)
	return historyDB, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Download History
// ─────────────────────────────────────────────────────────────────────────────

func AddHistoryItem(item HistoryItem, appName string) error {
	db, err := getHistoryDB()
	if err != nil {
		fmt.Printf("[History] AddHistoryItem skipped: %v\n", err)
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(historyBucket))
		if err != nil {
			return err
		}
		id, _ := b.NextSequence()
		item.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), id)
		item.Timestamp = time.Now().Unix()

		buf, err := json.Marshal(item)
		if err != nil {
			return err
		}

		if b.Stats().KeyN >= maxHistory {
			c := b.Cursor()
			toDelete := maxHistory / 20
			if toDelete < 1 {
				toDelete = 1
			}
			count := 0
			for k, _ := c.First(); k != nil && count < toDelete; k, _ = c.Next() {
				b.Delete(k)
				count++
			}
		}

		return b.Put([]byte(item.ID), buf)
	})
}

// FIX #4 — userID filtre les items. "" = admin (voit tout).
// Les items sans UserID (migrés) sont visibles par tous pour compatibilité.
func GetHistoryItems(appName string, userID string) ([]HistoryItem, error) {
	db, err := getHistoryDB()
	if err != nil {
		return []HistoryItem{}, nil
	}
	var items []HistoryItem
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var item HistoryItem
			if err := json.Unmarshal(v, &item); err == nil {
				// Montrer si : admin (userID vide), item legacy (UserID vide), ou item du user
				if userID == "" || item.UserID == "" || item.UserID == userID {
					items = append(items, item)
				}
			}
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})
	return items, err
}

// FIX — supprime clé par clé au lieu de détruire le bucket
// userID vide = suppression globale (admin)
func ClearHistory(appName string, userID string) error {
	db, err := getHistoryDB()
	if err != nil {
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if userID == "" {
				toDelete = append(toDelete, k)
			} else {
				var item HistoryItem
				if err := json.Unmarshal(v, &item); err == nil {
					if item.UserID == userID || item.UserID == "" {
						toDelete = append(toDelete, k)
					}
				}
			}
		}
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
}

// FIX #4 — vérifie l'ownership avant suppression
func DeleteHistoryItem(id string, appName string, userID string) error {
	db, err := getHistoryDB()
	if err != nil {
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		// Vérifier ownership si userID fourni
		if userID != "" {
			data := b.Get([]byte(id))
			if data != nil {
				var item HistoryItem
				if err := json.Unmarshal(data, &item); err == nil {
					if item.UserID != "" && item.UserID != userID {
						return fmt.Errorf("access denied")
					}
				}
			}
		}
		return b.Delete([]byte(id))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Fetch History
// ─────────────────────────────────────────────────────────────────────────────

func AddFetchHistoryItem(item FetchHistoryItem, appName string) error {
	db, err := getHistoryDB()
	if err != nil {
		fmt.Printf("[History] AddFetchHistoryItem skipped: %v\n", err)
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket))
		if err != nil {
			return err
		}

		// Dédupliquer par URL+Type+UserID
		if item.URL != "" {
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				var existing FetchHistoryItem
				if err := json.Unmarshal(v, &existing); err == nil {
					if existing.URL == item.URL && existing.Type == item.Type && existing.UserID == item.UserID {
						b.Delete(k)
					}
				}
			}
		}

		id, _ := b.NextSequence()
		item.ID = fmt.Sprintf("%d-%d", time.Now().UnixNano(), id)
		item.Timestamp = time.Now().Unix()

		buf, err := json.Marshal(item)
		if err != nil {
			return err
		}

		if b.Stats().KeyN >= maxHistory {
			c := b.Cursor()
			toDelete := maxHistory / 20
			if toDelete < 1 {
				toDelete = 1
			}
			count := 0
			for k, _ := c.First(); k != nil && count < toDelete; k, _ = c.Next() {
				b.Delete(k)
				count++
			}
		}

		return b.Put([]byte(item.ID), buf)
	})
}

// FIX #4 — filtrage par userID
func GetFetchHistoryItems(appName string, userID string) ([]FetchHistoryItem, error) {
	db, err := getHistoryDB()
	if err != nil {
		return []FetchHistoryItem{}, nil
	}
	var items []FetchHistoryItem
	err = db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var item FetchHistoryItem
			if err := json.Unmarshal(v, &item); err == nil {
				if userID == "" || item.UserID == "" || item.UserID == userID {
					items = append(items, item)
				}
			}
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})
	return items, err
}

// FIX — clé par clé + filtrage par user
func ClearFetchHistory(appName string, userID string) error {
	db, err := getHistoryDB()
	if err != nil {
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if userID == "" {
				toDelete = append(toDelete, k)
			} else {
				var item FetchHistoryItem
				if err := json.Unmarshal(v, &item); err == nil {
					if item.UserID == userID || item.UserID == "" {
						toDelete = append(toDelete, k)
					}
				}
			}
		}
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
}

func ClearFetchHistoryByType(itemType string, appName string, userID string) error {
	db, err := getHistoryDB()
	if err != nil {
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		var toDelete [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var item FetchHistoryItem
			if err := json.Unmarshal(v, &item); err == nil && item.Type == itemType {
				if userID == "" || item.UserID == userID || item.UserID == "" {
					toDelete = append(toDelete, k)
				}
			}
		}
		for _, k := range toDelete {
			b.Delete(k)
		}
		return nil
	})
}

func DeleteFetchHistoryItem(id string, appName string, userID string) error {
	db, err := getHistoryDB()
	if err != nil {
		return nil
	}
	return db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(fetchHistoryBucket))
		if b == nil {
			return nil
		}
		if userID != "" {
			data := b.Get([]byte(id))
			if data != nil {
				var item FetchHistoryItem
				if err := json.Unmarshal(data, &item); err == nil {
					if item.UserID != "" && item.UserID != userID {
						return fmt.Errorf("access denied")
					}
				}
			}
		}
		return b.Delete([]byte(id))
	})
}

// InitHistoryDBShared réutilise une instance BoltDB existante (ex: jobs.db)
func InitHistoryDBShared(db *bolt.DB) error {
	historyMu.Lock()
	defer historyMu.Unlock()

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(historyBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(fetchHistoryBucket)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return fmt.Errorf("history bucket init failed: %w", err)
	}

	historyDB = db
	historyDisabled = false
	historyShared = true
	fmt.Printf("[History] Using shared DB (no separate history.db)\n")
	return nil
}
