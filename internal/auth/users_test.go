package auth

import (
	"encoding/json"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestGetOrCreateUserBumpsTokenVersionOnPrivilegeChange(t *testing.T) {
	am := newTestAuthManager(t)

	p1, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (create): %v", err)
	}
	if p1.TokenVersion != 0 {
		t.Fatalf("new user TokenVersion = %d, want 0", p1.TokenVersion)
	}

	// Re-sync with the same admin flag: no privilege change, no bump.
	p2, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (no change): %v", err)
	}
	if p2.TokenVersion != 0 {
		t.Fatalf("TokenVersion after unchanged re-sync = %d, want 0", p2.TokenVersion)
	}

	// Jellyfin promotes the user to admin: privilege change, must bump so
	// any JWT issued before this point (still carrying admin=false, or an
	// old admin=true from a prior promotion/demotion cycle) stops matching.
	p3, err := am.GetOrCreateUser("jf-1", "Alice", true)
	if err != nil {
		t.Fatalf("GetOrCreateUser (promote): %v", err)
	}
	if p3.TokenVersion != 1 {
		t.Fatalf("TokenVersion after promotion = %d, want 1", p3.TokenVersion)
	}

	// Demoted back: another privilege change, another bump.
	p4, err := am.GetOrCreateUser("jf-1", "Alice", false)
	if err != nil {
		t.Fatalf("GetOrCreateUser (demote): %v", err)
	}
	if p4.TokenVersion != 2 {
		t.Fatalf("TokenVersion after demotion = %d, want 2", p4.TokenVersion)
	}
}

// TestGetOrCreateUserHealsMissingID is the regression test for a real
// production bug: a profile persisted under BoltDB key jellyfinID whose
// JSON blob has ID="" baked in (e.g. from before the ID field existed, or
// any other historical write that lost it) stayed permanently ID="" —
// every subsequent login refreshed Name/DisplayName/IsAdmin/UpdatedAt but
// never re-derived ID from the lookup key itself. A real Jellyfin admin
// hit this: their session correctly showed is_admin=true, but any API key
// they created inherited UserID="" and ValidateAPIKey's GetUser("") always
// failed, silently downgrading every admin-scoped key to non-admin.
func TestGetOrCreateUserHealsMissingID(t *testing.T) {
	am := newTestAuthManager(t)

	corrupted, err := json.Marshal(UserProfile{
		ID:          "", // the bug: stored blob has no ID even though the BoltDB key does
		Name:        "jf-legacy",
		DisplayName: "Legacy Admin",
		IsAdmin:     true,
		Settings:    make(map[string]interface{}),
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := am.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(BucketUsers)
		return b.Put([]byte("jf-legacy"), corrupted)
	}); err != nil {
		t.Fatalf("seed corrupted profile: %v", err)
	}

	profile, err := am.GetOrCreateUser("jf-legacy", "Legacy Admin", true)
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if profile.ID != "jf-legacy" {
		t.Fatalf("GetOrCreateUser did not heal ID: got %q, want %q", profile.ID, "jf-legacy")
	}

	// The real-world symptom: ValidateAPIKey looks up the profile via
	// GetUser(found.UserID) — this must now succeed and report IsAdmin.
	healed, err := am.GetUser("jf-legacy")
	if err != nil {
		t.Fatalf("GetUser after healing: %v", err)
	}
	if !healed.IsAdmin {
		t.Fatalf("healed profile IsAdmin = false, want true")
	}
}

// TestSaveUserSettingsSetsIDOnFirstWrite covers the same bug class as
// TestGetOrCreateUserHealsMissingID, found by auditing every other writer
// of bucketUsers after fixing GetOrCreateUser: SaveUserSettings is the
// sole writer for a userID that has never logged in through
// GetOrCreateUser (e.g. the local-admin bypass profile, never persisted
// by design) — without setting ID explicitly here too, that first write
// would freeze ID="" forever, same as the original bug.
func TestSaveUserSettingsSetsIDOnFirstWrite(t *testing.T) {
	am := newTestAuthManager(t)

	if err := am.SaveUserSettings("u2", map[string]interface{}{"theme": "dark"}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}
	profile, err := am.GetUser("u2")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if profile.ID != "u2" {
		t.Fatalf("profile.ID = %q, want %q", profile.ID, "u2")
	}
}
