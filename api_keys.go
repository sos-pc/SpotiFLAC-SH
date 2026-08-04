package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sos-pc/SpotiFLAC-SH/backend/util"
	bolt "go.etcd.io/bbolt"
)

// ─────────────────────────────────────────────────────────────────────────────
// Types
// ─────────────────────────────────────────────────────────────────────────────

const apiKeyPrefix = "sk_spotiflac_"

var bucketAPIKeys = []byte("apikeys")

// APIKey représente une clé API persistée en BoltDB.
// KeyHash est sha256hex(rawKey) — la clé brute n'est jamais stockée.
type APIKey struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	KeyHash     string    `json:"key_hash"`
	UserID      string    `json:"user_id"`
	Permissions []string  `json:"permissions"` // "read", "manage", "admin" ("download" is a pre-rename legacy alias for "manage", see v1RequirePermission)
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

func hashAPIKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}

// ─────────────────────────────────────────────────────────────────────────────
// CRUD
// ─────────────────────────────────────────────────────────────────────────────

// CreateAPIKey génère une clé brute (192 bits, haute entropie), la persiste
// hashée (SHA-256), et retourne la clé brute une seule fois.
func (a *AuthManager) CreateAPIKey(userID, name string, permissions []string) (string, *APIKey, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("failed to generate key: %v", err)
	}
	rawKey := apiKeyPrefix + hex.EncodeToString(buf)

	idBuf := make([]byte, 16)
	if _, err := rand.Read(idBuf); err != nil {
		return "", nil, fmt.Errorf("failed to generate key ID: %v", err)
	}
	key := &APIKey{
		ID:          "key-" + hex.EncodeToString(idBuf),
		Name:        name,
		KeyHash:     hashAPIKey(rawKey),
		UserID:      userID,
		Permissions: permissions,
		CreatedAt:   time.Now(),
	}

	err := a.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucketAPIKeys)
		if err != nil {
			return err
		}
		data, err := json.Marshal(key)
		if err != nil {
			return err
		}
		return b.Put([]byte(key.ID), data)
	})
	if err != nil {
		return "", nil, err
	}
	return rawKey, key, nil
}

// ListAPIKeys retourne les clés de l'utilisateur sans exposer KeyHash.
func (a *AuthManager) ListAPIKeys(userID string) ([]APIKey, error) {
	var keys []APIKey
	err := a.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPIKeys)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var key APIKey
			if err := json.Unmarshal(v, &key); err == nil && key.UserID == userID {
				key.KeyHash = ""
				keys = append(keys, key)
			}
			return nil
		})
	})
	return keys, err
}

// RevokeAPIKey supprime une clé (seul le propriétaire peut révoquer).
func (a *AuthManager) RevokeAPIKey(keyID, userID string) error {
	return a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPIKeys)
		if b == nil {
			return fmt.Errorf("key not found")
		}
		data := b.Get([]byte(keyID))
		if data == nil {
			return fmt.Errorf("key not found")
		}
		var key APIKey
		if err := json.Unmarshal(data, &key); err != nil {
			return err
		}
		if key.UserID != userID {
			return fmt.Errorf("access denied")
		}
		return b.Delete([]byte(keyID))
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation
// ─────────────────────────────────────────────────────────────────────────────

// ValidateAPIKey vérifie une clé brute par comparaison SHA-256 et retourne
// les claims si valide. Scan linéaire O(n) — performant pour n < 50 clés.
func (a *AuthManager) ValidateAPIKey(rawKey string) (*JWTClaims, bool) {
	if a == nil || rawKey == "" {
		return nil, false
	}
	hash := hashAPIKey(rawKey)

	var found *APIKey
	a.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPIKeys)
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			if found != nil {
				return nil
			}
			var key APIKey
			if err := json.Unmarshal(v, &key); err == nil && hmac.Equal([]byte(key.KeyHash), []byte(hash)) {
				found = &key
			}
			return nil
		})
	})

	if found == nil {
		return nil, false
	}

	util.SafeGo("apikeys.touch["+found.ID+"]", func() { a.touchAPIKey(found.ID) })

	isAdmin := false
	for _, p := range found.Permissions {
		if p == "admin" {
			isAdmin = true
			break
		}
	}
	// A key's stored "admin" permission reflects what its owning account
	// could do at creation time, not now. Re-check against the account's
	// current status so a demoted admin (or a key that predates
	// v1CreateAPIKey's self-escalation guard) can't keep admin API access
	// forever through a key that never expires. Only the admin claim is
	// downgraded on a stale/missing account — the key's other permissions
	// (read/download) still apply.
	if isAdmin {
		if profile, err := a.GetUser(found.UserID); err != nil || !profile.IsAdmin {
			isAdmin = false
		}
	}
	return &JWTClaims{
		UserID:      found.UserID,
		DisplayName: "API Key: " + found.Name,
		IsAdmin:     isAdmin,
		ExpiresAt:   0, // les clés API n'expirent pas
		Permissions: found.Permissions,
		IsAPIKey:    true,
	}, true
}

func (a *AuthManager) touchAPIKey(keyID string) {
	a.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAPIKeys)
		if b == nil {
			return nil
		}
		data := b.Get([]byte(keyID))
		if data == nil {
			return nil
		}
		var key APIKey
		if err := json.Unmarshal(data, &key); err != nil {
			return nil
		}
		key.LastUsedAt = time.Now()
		updated, _ := json.Marshal(key)
		return b.Put([]byte(keyID), updated)
	})
}
