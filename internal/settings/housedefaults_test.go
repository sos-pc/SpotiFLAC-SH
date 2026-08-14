package settings

import (
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/config"
)

// After the migration seeded house defaults there was no way to change them: a
// personal key submitted by an admin goes to their own profile, by design, so
// the instance store's copy stayed frozen at whatever the migration found.
func TestPublishHouseDefaultsUpdatesWhatNewcomersGet(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{
		"tidalQuality": "LOSSLESS", // what the migration seeded
		"downloadPath": "/home/nonroot/Music",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{
		"tidalQuality": "HI_RES_LOSSLESS", // the admin has since changed theirs
		"embedLyrics":  true,
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	n, err := PublishHouseDefaults(am, "admin")
	if err != nil {
		t.Fatalf("PublishHouseDefaults: %v", err)
	}
	if n == 0 {
		t.Fatal("published nothing while the admin's quality differed from the default")
	}

	if _, err := am.GetOrCreateUser("newcomer", "newcomer", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	blob := EffectiveBlob(am, "newcomer")
	if got := blob["tidalQuality"]; got != "HI_RES_LOSSLESS" {
		t.Errorf("newcomer's tidalQuality = %v, want the operator's current HI_RES_LOSSLESS", got)
	}
	if got := blob["embedLyrics"]; got != true {
		t.Errorf("newcomer's embedLyrics = %v, want true", got)
	}
}

// Publishing must not touch the instance-scoped keys. They are not defaults —
// they are the value, for everyone — and rewriting them from a resolved blob
// would be a different operation wearing the same button.
func TestPublishHouseDefaultsLeavesInstanceKeysAlone(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{
		"downloadPath": "/operator/choice",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{
		"downloadPath": "/somewhere/else", // stale, and ignored on read
		"autoQuality":  "24",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if _, err := PublishHouseDefaults(am, "admin"); err != nil {
		t.Fatalf("PublishHouseDefaults: %v", err)
	}
	instance, _ := config.LoadSettingsFile()
	if instance["downloadPath"] != "/operator/choice" {
		t.Errorf("downloadPath = %v, want it untouched", instance["downloadPath"])
	}
	if instance["autoQuality"] != "24" {
		t.Errorf("autoQuality = %v, want the published 24", instance["autoQuality"])
	}
}

// Pressing the button twice must report nothing to do rather than rewriting the
// file, so "already matches" is a real answer and not a silent no-op.
func TestPublishHouseDefaultsIsIdempotent(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{"autoQuality": "24"}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	first, err := PublishHouseDefaults(am, "admin")
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first == 0 {
		t.Fatal("first publish wrote nothing")
	}
	second, err := PublishHouseDefaults(am, "admin")
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if second != 0 {
		t.Errorf("second publish reported %d changes, want 0", second)
	}
}

// HouseDefaults shows what a newcomer starts from, which is the personal keys
// only — an instance key is not a default anyone can depart from.
func TestHouseDefaultsExcludesInstanceKeys(t *testing.T) {
	isolate(t)
	if err := config.SaveSettingsFile(map[string]interface{}{
		"autoQuality":  "24",
		"downloadPath": "/home/nonroot/Music",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	got, err := HouseDefaults()
	if err != nil {
		t.Fatalf("HouseDefaults: %v", err)
	}
	if _, present := got["downloadPath"]; present {
		t.Error("an instance-scoped key was reported as a default")
	}
	if got["autoQuality"] != "24" {
		t.Errorf("autoQuality = %v, want 24", got["autoQuality"])
	}
}
