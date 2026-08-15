package settings

import (
	"encoding/json"
	"sort"
)

// ─────────────────────────────────────────────────────────────────────────────
// Who owns which setting
// ─────────────────────────────────────────────────────────────────────────────
//
// The settings blob is a map because it is the frontend's, not ours: of its 29
// keys the backend interprets 19, and the other ten are pure interface state —
// theme, font, sound effects, the preset selectors. They must survive a
// round-trip untouched, which is why this file labels keys rather than
// replacing the map with a struct that would drop everything it does not know.
//
// Scope is declared HERE and nowhere else. A key absent from this table is
// user-owned by default, which is the right answer for the ten interface keys
// and for anything the frontend adds later without telling us.
//
// The rule that decides a new key, so it classifies itself: **anything that
// decides where a file lands on the shared disk, or names shared
// infrastructure, is instance.** Everything else is user.
//
// This matters because a file's location is computed from the settings of
// whoever enqueued the job, and the skip check that prevents re-downloading
// looks in that same computed place. Two users differing on one of these nine
// download the same FLAC twice into a library Jellyfin then shows as
// duplicated. It has already happened here across time rather than across
// people: on the reference deployment, 15 jobs carry createPlaylistFolder=false
// against 394 carrying true.

type Scope uint8

const (
	// ScopeUser — each account keeps its own value. The default for any key
	// not named below.
	ScopeUser Scope = iota
	// ScopeInstance — one value for the whole deployment, admin-writable only.
	ScopeInstance
)

// instanceKeys is the whole of the instance scope. Nine keys.
//
// Note what is NOT here. Qualities and the embed toggles stay user-owned: the
// preference is genuinely personal even though its effect is bounded by
// deduplication — someone downloading a track at LOSSLESS without lyrics means
// the next person gets that file whatever they asked for. That is a policy
// question (issue #73), not a scoping one, and moving them here would answer it
// by accident.
var instanceKeys = map[string]bool{
	// Decide where a file lands, or what it is called, on one shared disk.
	"downloadPath":         true,
	"folderTemplate":       true,
	"createPlaylistFolder": true,
	"filenameTemplate":     true,
	"trackNumber":          true, // appears in the filename
	"useFirstArtistOnly":   true, // appears in both the folder and the filename

	// Name shared infrastructure.
	"jellyfinMusicPath": true, // one Jellyfin
	"spotFetchAPIUrl":   true, // one fallback endpoint, and it is a third party
	"createM3u8File":    true, // whether M3U8s are written into that one Jellyfin
}

// retiredKeys are keys a past version wrote and this one no longer knows. The
// value is why, so the next reader does not have to dig through git to find out
// whether the key is dead or merely undocumented.
//
// They matter because the settings blob is an untyped map: a key nobody writes
// any more is not dropped by a struct field disappearing. It sits in config.json
// and in user profiles, gets merged into every effective blob, and ships to
// every client — forever, unless something removes it. PromoteInstanceSettings
// does, at startup.
var retiredKeys = map[string]string{
	"spotifyClientId": "the per-account Spotify connection was removed in #92",
}

// notSettings are the field names of the settings API's own response envelope.
// They are never settings. A blob containing one is a client that has PUT back
// what it received from GET instead of the values inside it.
//
// Not hypothetical. On the reference deployment, 2026-08-15: the operator's
// profile acquired "values", "instanceKeys" and "writableScope";
// PromoteInstanceSettings dutifully seeded all three into config.json as house
// defaults; and "values" carried a complete second copy of every setting —
// including a key the same startup pass had just retired from the top level.
// The retirement worked and was useless, because the key had a twin one level
// down.
//
// Refused on write and stripped at startup. A store that accepts anything
// eventually contains everything, and none of it can be reasoned about.
var notSettings = map[string]bool{
	"values":        true,
	"instanceKeys":  true,
	"writableScope": true,
}

// IsNotASetting reports whether a key is one the settings store must never
// hold, whatever a client says. Callers that WRITE use this to refuse; callers
// that clean up use DiscardReason, which also covers retired keys.
func IsNotASetting(key string) bool { return notSettings[key] }

// NotSettingKeys returns, sorted, the submitted keys that are envelope fields
// rather than settings. Empty means the submission is well-formed.
func NotSettingKeys(blob map[string]interface{}) []string {
	var bad []string
	for k := range blob {
		if notSettings[k] {
			bad = append(bad, k)
		}
	}
	sort.Strings(bad)
	return bad
}

// DiscardReason reports whether a key must not be stored, and why.
//
// Two origins, one outcome: a key this version retired, and a key that was
// never a setting at all. Both are removed from the instance store and from
// every profile at startup.
func DiscardReason(key string) (string, bool) {
	if why, ok := retiredKeys[key]; ok {
		return why, true
	}
	if notSettings[key] {
		return "a field of the settings API's response envelope, never a setting", true
	}
	return "", false
}

// ScopeOf reports who owns key.
func ScopeOf(key string) Scope {
	if instanceKeys[key] {
		return ScopeInstance
	}
	return ScopeUser
}

// InstanceKeys returns the instance-scoped key names. Order is not guaranteed.
func InstanceKeys() []string {
	keys := make([]string, 0, len(instanceKeys))
	for k := range instanceKeys {
		keys = append(keys, k)
	}
	return keys
}

// SplitByScope divides a submitted blob into the part that may only be written
// by an admin and the part that belongs to the caller.
//
// It splits rather than rejecting the whole submission: the settings screen
// sends one object containing both kinds, and refusing it outright would mean a
// non-admin could not change their own theme because the same form also carries
// the download path.
func SplitByScope(blob map[string]interface{}) (instance, user map[string]interface{}) {
	instance = map[string]interface{}{}
	user = map[string]interface{}{}
	for k, v := range blob {
		if ScopeOf(k) == ScopeInstance {
			instance[k] = v
		} else {
			user[k] = v
		}
	}
	return instance, user
}

// SameValue reports whether two settings values are equivalent.
//
// It exists because the values arrive from JSON, where a number is a float64
// and nothing guarantees the stored form matches the submitted one. Comparing
// with == would report a difference between 6 and 6.0 and refuse an edit nobody
// made; reflect.DeepEqual has the same problem. Everything here is a string,
// bool or number, so rendering both sides through JSON and comparing the text
// is both correct and short.
func SameValue(a, b interface{}) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}
