package settings

import (
	"os"
	"testing"

	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/config"
	bolt "go.etcd.io/bbolt"
)

// isolate points AppDir — and therefore the instance store — at a temp
// directory, so these tests never touch a real config.json.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)        // unix
	t.Setenv("USERPROFILE", dir) // windows
	if got, err := config.SettingsFilePath(); err != nil || len(got) == 0 {
		t.Fatalf("SettingsFilePath after isolation: %q, %v", got, err)
	}
}

func newAuth(t *testing.T) *auth.AuthManager {
	t.Helper()
	f, err := os.CreateTemp("", "settings-test-*.db")
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

	am, err := auth.NewAuthManager(db)
	if err != nil {
		t.Fatalf("NewAuthManager: %v", err)
	}
	return am
}

// The defect this whole change exists to remove: resolution was a replacement,
// so a user who had saved ONE key lost the instance value of every other.
func TestUserPatchOverridesOnlyItsOwnKeys(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{
		"jellyfinMusicPath": "/Multimedia/Musique/Spotiflac",
		"autoQuality":       "24",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("u1", "user", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	// One key, nothing else.
	if err := am.SaveUserSettings("u1", map[string]interface{}{"embedLyrics": true}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	blob := EffectiveBlob(am, "u1")

	if got := blob["jellyfinMusicPath"]; got != "/Multimedia/Musique/Spotiflac" {
		t.Errorf("jellyfinMusicPath = %v; the instance layer was replaced rather than overlaid", got)
	}
	if got := blob["autoQuality"]; got != "24" {
		t.Errorf("autoQuality = %v, want the instance value 24", got)
	}
	if got := blob["embedLyrics"]; got != true {
		t.Errorf("embedLyrics = %v, want the user's own true", got)
	}
}

// A per-user downloadPath is what let a non-admin widen the root that confines
// them. Every profile written before this shipped has one, so ignoring it on
// read — not merely refusing new writes — is what closes it.
func TestStoredUserInstanceKeysAreIgnored(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{
		"downloadPath": "/home/nonroot/Music",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("u1", "user", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("u1", map[string]interface{}{
		"downloadPath": "/", // the whole filesystem
		"theme":        "yellow",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	blob := EffectiveBlob(am, "u1")
	if got := blob["downloadPath"]; got != "/home/nonroot/Music" {
		t.Errorf("downloadPath = %v; a stored per-user value is still being honoured", got)
	}
	if got := blob["theme"]; got != "yellow" {
		t.Errorf("theme = %v; a user-scoped key was dropped", got)
	}
}

// The ten interface-only keys must survive. Losing them is what made the
// original "replace the map with a struct" design unshippable.
func TestUnknownKeysAreUserScopedAndSurvive(t *testing.T) {
	isolate(t)
	if s := ScopeOf("fontFamily"); s != ScopeUser {
		t.Errorf("fontFamily scope = %v, want user", s)
	}
	if s := ScopeOf("somethingTheFrontendAddsNextYear"); s != ScopeUser {
		t.Errorf("unknown key scope = %v, want user", s)
	}

	instance, user := SplitByScope(map[string]interface{}{
		"downloadPath": "/x",
		"theme":        "yellow",
		"brandNewKey":  42,
	})
	if len(instance) != 1 || instance["downloadPath"] != "/x" {
		t.Errorf("instance part = %v", instance)
	}
	if len(user) != 2 || user["theme"] != "yellow" || user["brandNewKey"] != 42 {
		t.Errorf("user part = %v", user)
	}
}

// createM3u8File is the first setting whose default is true — unreachable in
// the old design, where a missing key read as false.
func TestCreateM3u8FileDefaultsOn(t *testing.T) {
	isolate(t)
	am := newAuth(t)
	if got := EffectiveBlob(am, "")["createM3u8File"]; got != true {
		t.Errorf("createM3u8File = %v on a fresh deployment, want true", got)
	}
}

// The migration. Without it, jellyfinMusicPath and spotFetchAPIUrl — which
// exist only in the admin's profile on the reference deployment — become empty,
// and M3U8 files silently stop landing where Jellyfin reads them.
func TestPromoteMovesInstanceKeysOutOfProfiles(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{
		"jellyfinMusicPath": "/Multimedia/Musique/Spotiflac",
		"downloadPath":      "/home/nonroot/Music",
		"theme":             "yellow",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("PromoteInstanceSettings: %v", err)
	}

	instance, err := config.LoadSettingsFile()
	if err != nil {
		t.Fatalf("LoadSettingsFile: %v", err)
	}
	if instance["jellyfinMusicPath"] != "/Multimedia/Musique/Spotiflac" {
		t.Errorf("jellyfinMusicPath was not promoted: %v", instance["jellyfinMusicPath"])
	}
	if instance["downloadPath"] != "/home/nonroot/Music" {
		t.Errorf("downloadPath was not promoted: %v", instance["downloadPath"])
	}

	profile, err := am.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if _, still := profile.Settings["jellyfinMusicPath"]; still {
		t.Error("instance key left in the profile; the next reader who forgets the filter finds it")
	}
	if profile.Settings["theme"] != "yellow" {
		t.Error("a user-scoped key was stripped along with the instance ones")
	}

	// Idempotent: a second run must change nothing.
	before := instance["downloadPath"]
	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("second PromoteInstanceSettings: %v", err)
	}
	after, _ := config.LoadSettingsFile()
	if after["downloadPath"] != before {
		t.Errorf("second run changed downloadPath: %v -> %v", before, after["downloadPath"])
	}
}

// A household member's download path must not silently become house policy.
func TestPromoteIgnoresNonAdminProfiles(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if _, err := am.GetOrCreateUser("u1", "regular", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("u1", map[string]interface{}{"downloadPath": "/tmp/theirs"}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("PromoteInstanceSettings: %v", err)
	}

	instance, _ := config.LoadSettingsFile()
	if instance["downloadPath"] == "/tmp/theirs" {
		t.Error("a non-admin's download path was promoted to instance scope")
	}
	// It is still stripped from their profile — it stopped applying either way.
	profile, _ := am.GetUser("u1")
	if _, still := profile.Settings["downloadPath"]; still {
		t.Error("instance key left in a non-admin profile")
	}
}

// An operator value already set must never be overwritten by a profile copy.
func TestPromoteNeverOverwritesTheInstanceStore(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{"downloadPath": "/operator/choice"}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{"downloadPath": "/stale/copy"}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("PromoteInstanceSettings: %v", err)
	}
	instance, _ := config.LoadSettingsFile()
	if instance["downloadPath"] != "/operator/choice" {
		t.Errorf("downloadPath = %v, want the operator's existing value", instance["downloadPath"])
	}
}

func TestSameValueToleratesJSONNumberForms(t *testing.T) {
	if !SameValue(float64(6), 6) {
		t.Error("6.0 and 6 reported as different; an unchanged edit would be refused")
	}
	if SameValue("a", "b") {
		t.Error("distinct strings reported as equal")
	}
	if !SameValue(nil, nil) {
		t.Error("nil and nil reported as different")
	}
	if SameValue(nil, "x") {
		t.Error("nil and a value reported as equal")
	}
}

// A new household member used to start from package defaults, not from the
// operator's configuration: LOSSLESS where the admin had HI_RES_LOSSLESS, and
// lyrics, genre and provider fallback all off where the admin had them on. They
// would have downloaded worse files than the person who invited them, with
// nothing saying so.
func TestPromoteSeedsHouseDefaultsForPersonalKeys(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{
		"tidalQuality":      "HI_RES_LOSSLESS",
		"embedLyrics":       true,
		"jellyfinMusicPath": "/Multimedia/Musique/Spotiflac",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("PromoteInstanceSettings: %v", err)
	}

	// A brand-new account, with no settings of its own.
	if _, err := am.GetOrCreateUser("newcomer", "newcomer", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	blob := EffectiveBlob(am, "newcomer")

	if got := blob["tidalQuality"]; got != "HI_RES_LOSSLESS" {
		t.Errorf("tidalQuality = %v, want the operator's HI_RES_LOSSLESS", got)
	}
	if got := blob["embedLyrics"]; got != true {
		t.Errorf("embedLyrics = %v, want the operator's true", got)
	}

	// The admin keeps their own copy: these are user-scoped, only the
	// instance-scoped ones are stripped.
	profile, err := am.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if profile.Settings["tidalQuality"] != "HI_RES_LOSSLESS" {
		t.Error("seeding removed a personal key from the admin's own profile")
	}
	if _, still := profile.Settings["jellyfinMusicPath"]; still {
		t.Error("an instance-scoped key survived in the profile")
	}
}

// Seeding must not silently make a personal preference unchangeable: whoever
// sets their own value still wins over the house default.
func TestHouseDefaultsYieldToAPersonalChoice(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	if err := config.SaveSettingsFile(map[string]interface{}{
		"tidalQuality": "HI_RES_LOSSLESS", // seeded house default
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}
	if _, err := am.GetOrCreateUser("u1", "user", false); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("u1", map[string]interface{}{
		"tidalQuality": "LOSSLESS",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}

	if got := EffectiveBlob(am, "u1")["tidalQuality"]; got != "LOSSLESS" {
		t.Errorf("tidalQuality = %v, want the user's own LOSSLESS", got)
	}
}

// A key no version writes any more must leave, from both stores. The settings
// blob is an untyped map, so nothing removes it on its own: it would keep
// riding along in every effective blob sent to every client, and in the case
// that motivated this — spotifyClientId, left behind by the Spotify connection
// removed in #92 — it would keep publishing the id of an application the
// operator has decommissioned.
func TestPromoteRemovesRetiredKeys(t *testing.T) {
	isolate(t)
	am := newAuth(t)

	retired := ""
	for k := range retiredKeys {
		retired = k
		break
	}
	if retired == "" {
		t.Skip("no key is currently retired")
	}

	if _, err := am.GetOrCreateUser("admin", "admin", true); err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if err := am.SaveUserSettings("admin", map[string]interface{}{
		retired: "left over in a profile",
		"theme": "yellow",
	}); err != nil {
		t.Fatalf("SaveUserSettings: %v", err)
	}
	if err := config.SaveSettingsFile(map[string]interface{}{
		retired:        "left over in the instance store",
		"downloadPath": "/home/nonroot/Music",
	}); err != nil {
		t.Fatalf("SaveSettingsFile: %v", err)
	}

	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("PromoteInstanceSettings: %v", err)
	}

	instance, err := config.LoadSettingsFile()
	if err != nil {
		t.Fatalf("LoadSettingsFile: %v", err)
	}
	if _, still := instance[retired]; still {
		t.Errorf("%s survived in the instance store", retired)
	}
	if instance["downloadPath"] != "/home/nonroot/Music" {
		t.Errorf("a live key was lost with the retired one: %v", instance["downloadPath"])
	}

	profile, err := am.GetUser("admin")
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if _, still := profile.Settings[retired]; still {
		t.Errorf("%s survived in the profile", retired)
	}
	if profile.Settings["theme"] != "yellow" {
		t.Error("a user-scoped key was stripped along with the retired one")
	}

	// Idempotent: nothing left to remove, and nothing else removed either.
	if err := PromoteInstanceSettings(am); err != nil {
		t.Fatalf("second PromoteInstanceSettings: %v", err)
	}
	again, _ := config.LoadSettingsFile()
	if again["downloadPath"] != "/home/nonroot/Music" {
		t.Errorf("second run changed downloadPath: %v", again["downloadPath"])
	}
}
