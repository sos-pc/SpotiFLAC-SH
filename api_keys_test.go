package main

import (
	"os"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// newTestAuthManager crée un AuthManager avec une BoltDB temporaire.
func newTestAuthManager(t *testing.T) *AuthManager {
	t.Helper()
	f, err := os.CreateTemp("", "spotiflac-test-*.db")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })

	db, err := bolt.Open(f.Name(), 0600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	am, err := NewAuthManager(db)
	if err != nil {
		t.Fatalf("NewAuthManager: %v", err)
	}
	return am
}

// ─── hashAPIKey ───────────────────────────────────────────────────────────────

func TestHashAPIKey(t *testing.T) {
	t.Run("déterministe", func(t *testing.T) {
		h1 := hashAPIKey("sk_spotiflac_abc123")
		h2 := hashAPIKey("sk_spotiflac_abc123")
		if h1 != h2 {
			t.Error("hashAPIKey devrait être déterministe")
		}
	})

	t.Run("longueur SHA-256 hex = 64 chars", func(t *testing.T) {
		h := hashAPIKey("quelcle")
		if len(h) != 64 {
			t.Errorf("longueur = %d, want 64", len(h))
		}
	})

	t.Run("deux clés différentes → hashes différents", func(t *testing.T) {
		h1 := hashAPIKey("sk_spotiflac_aaa")
		h2 := hashAPIKey("sk_spotiflac_bbb")
		if h1 == h2 {
			t.Error("clés différentes ne doivent pas avoir le même hash")
		}
	})
}

// ─── CreateAPIKey ─────────────────────────────────────────────────────────────

func TestCreateAPIKey(t *testing.T) {
	am := newTestAuthManager(t)

	rawKey, key, err := am.CreateAPIKey("user1", "ma-clé", []string{"read", "download"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	t.Run("clé brute commence par le préfixe", func(t *testing.T) {
		if !strings.HasPrefix(rawKey, apiKeyPrefix) {
			t.Errorf("rawKey %q ne commence pas par %q", rawKey, apiKeyPrefix)
		}
	})

	t.Run("clé brute non vide et assez longue", func(t *testing.T) {
		// préfixe (13) + 48 chars hex (24 bytes) = 61 chars minimum
		if len(rawKey) < 60 {
			t.Errorf("rawKey trop court: %d chars", len(rawKey))
		}
	})

	t.Run("KeyHash est sha256(rawKey)", func(t *testing.T) {
		if key.KeyHash != hashAPIKey(rawKey) {
			t.Error("KeyHash ne correspond pas au hash de la clé brute")
		}
	})

	t.Run("ID non vide et préfixé par key-", func(t *testing.T) {
		if !strings.HasPrefix(key.ID, "key-") {
			t.Errorf("ID = %q, devrait commencer par 'key-'", key.ID)
		}
	})

	t.Run("deux créations → IDs différents", func(t *testing.T) {
		_, k2, err := am.CreateAPIKey("user1", "autre", []string{"read"})
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		if key.ID == k2.ID {
			t.Error("deux clés créées ont le même ID")
		}
	})

	t.Run("clé brute jamais stockée en clair", func(t *testing.T) {
		keys, err := am.ListAPIKeys("user1")
		if err != nil {
			t.Fatalf("ListAPIKeys: %v", err)
		}
		for _, k := range keys {
			if k.KeyHash == rawKey {
				t.Error("la clé brute est stockée en clair dans KeyHash")
			}
			if k.KeyHash != "" {
				t.Errorf("ListAPIKeys a exposé KeyHash: %q", k.KeyHash)
			}
		}
	})
}

// ─── ValidateAPIKey ───────────────────────────────────────────────────────────

func TestValidateAPIKey(t *testing.T) {
	am := newTestAuthManager(t)

	rawKey, _, err := am.CreateAPIKey("user42", "test", []string{"read", "download"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	t.Run("clé valide retourne claims corrects", func(t *testing.T) {
		claims, ok := am.ValidateAPIKey(rawKey)
		if !ok {
			t.Fatal("ValidateAPIKey devrait retourner true")
		}
		if claims.UserID != "user42" {
			t.Errorf("UserID = %q, want %q", claims.UserID, "user42")
		}
		if claims.IsAdmin {
			t.Error("IsAdmin devrait être false sans permission admin")
		}
	})

	t.Run("clé invalide retourne false", func(t *testing.T) {
		_, ok := am.ValidateAPIKey("sk_spotiflac_invalide")
		if ok {
			t.Error("ValidateAPIKey devrait retourner false pour clé invalide")
		}
	})

	t.Run("clé vide retourne false", func(t *testing.T) {
		_, ok := am.ValidateAPIKey("")
		if ok {
			t.Error("ValidateAPIKey devrait retourner false pour clé vide")
		}
	})

	t.Run("permission admin sur un compte admin → IsAdmin=true", func(t *testing.T) {
		if _, err := am.GetOrCreateUser("admin1", "Admin One", true); err != nil {
			t.Fatalf("GetOrCreateUser: %v", err)
		}
		adminKey, _, err := am.CreateAPIKey("admin1", "admin", []string{"read", "admin"})
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		claims, ok := am.ValidateAPIKey(adminKey)
		if !ok {
			t.Fatal("ValidateAPIKey devrait retourner true")
		}
		if !claims.IsAdmin {
			t.Error("IsAdmin devrait être true avec permission admin sur un compte réellement admin")
		}
	})

	// Regression test: a key's stored "admin" permission must not be
	// honored unless the owning account is CURRENTLY admin. Without this,
	// a key created before v1CreateAPIKey's self-escalation guard existed
	// (or an account later demoted from admin) would keep full admin API
	// access forever, since API keys never expire.
	t.Run("permission admin sur un compte non-admin → IsAdmin=false", func(t *testing.T) {
		if _, err := am.GetOrCreateUser("notadmin1", "Not Admin", false); err != nil {
			t.Fatalf("GetOrCreateUser: %v", err)
		}
		key, _, err := am.CreateAPIKey("notadmin1", "escalation attempt", []string{"read", "admin"})
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		claims, ok := am.ValidateAPIKey(key)
		if !ok {
			t.Fatal("ValidateAPIKey devrait retourner true (la clé reste valide pour read/download)")
		}
		if claims.IsAdmin {
			t.Error("IsAdmin devrait être false : le compte propriétaire n'est pas admin, malgré la permission stockée")
		}
	})

	t.Run("permission admin sur un compte inconnu (bypass local non persisté) → IsAdmin=false", func(t *testing.T) {
		key, _, err := am.CreateAPIKey("ghost-user", "orphan", []string{"admin"})
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		claims, ok := am.ValidateAPIKey(key)
		if !ok {
			t.Fatal("ValidateAPIKey devrait retourner true")
		}
		if claims.IsAdmin {
			t.Error("IsAdmin devrait être false : impossible de vérifier un compte qui n'existe pas")
		}
	})

	t.Run("admin rétrogradé perd IsAdmin sur ses clés existantes", func(t *testing.T) {
		if _, err := am.GetOrCreateUser("admin2", "Admin Two", true); err != nil {
			t.Fatalf("GetOrCreateUser (create admin): %v", err)
		}
		key, _, err := am.CreateAPIKey("admin2", "admin key", []string{"admin"})
		if err != nil {
			t.Fatalf("CreateAPIKey: %v", err)
		}
		if claims, ok := am.ValidateAPIKey(key); !ok || !claims.IsAdmin {
			t.Fatalf("clé devrait être admin avant rétrogradation (ok=%v)", ok)
		}

		if _, err := am.GetOrCreateUser("admin2", "Admin Two", false); err != nil {
			t.Fatalf("GetOrCreateUser (démotion): %v", err)
		}

		claims, ok := am.ValidateAPIKey(key)
		if !ok {
			t.Fatal("ValidateAPIKey devrait retourner true")
		}
		if claims.IsAdmin {
			t.Error("IsAdmin devrait être false après rétrogradation du compte propriétaire")
		}
	})
}

// ─── RevokeAPIKey ─────────────────────────────────────────────────────────────

func TestRevokeAPIKey(t *testing.T) {
	am := newTestAuthManager(t)

	rawKey, key, err := am.CreateAPIKey("user1", "à révoquer", []string{"read"})
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	t.Run("un autre user ne peut pas révoquer", func(t *testing.T) {
		err := am.RevokeAPIKey(key.ID, "user2")
		if err == nil {
			t.Error("RevokeAPIKey devrait refuser à un autre user")
		}
	})

	t.Run("le propriétaire peut révoquer", func(t *testing.T) {
		err := am.RevokeAPIKey(key.ID, "user1")
		if err != nil {
			t.Fatalf("RevokeAPIKey: %v", err)
		}
	})

	t.Run("clé révoquée n'est plus valide", func(t *testing.T) {
		_, ok := am.ValidateAPIKey(rawKey)
		if ok {
			t.Error("clé révoquée ne devrait plus être valide")
		}
	})

	t.Run("révoquer une clé inexistante → erreur", func(t *testing.T) {
		err := am.RevokeAPIKey("key-inexistant", "user1")
		if err == nil {
			t.Error("RevokeAPIKey devrait retourner une erreur pour clé inexistante")
		}
	})
}
