package settings

import (
	"log/slog"

	"github.com/sos-pc/SpotiFLAC-SH/internal/auth"
	"github.com/sos-pc/SpotiFLAC-SH/internal/config"
)

// PromoteInstanceSettings moves instance-scoped keys out of user profiles and
// into the instance store, once, at startup.
//
// It has to exist, and skipping it loses data silently. On the reference
// deployment there is no config.json at all — LoadSettingsFile returns
// (nil, nil) when the file is absent — so before this runs, EVERY setting
// resolves from the admin's profile. Two of them exist nowhere else:
//
//	jellyfinMusicPath : /Multimedia/Musique/Spotiflac
//	spotFetchAPIUrl   : https://spotify.afkarxyz.fun/api
//
// Ship the layered read without promoting them and both become empty. The first
// one stops M3U8 files landing where Jellyfin reads them — no error, nothing in
// the log, the playlists simply stop appearing.
//
// Three parts, in this order, and each is needed:
//
//   - Promote: for each instance-scoped key missing from the instance store but
//     present in an admin's profile, write it to the instance store.
//   - Seed: the same for the PERSONAL keys, as house defaults, so a new
//     account starts from the operator's configuration and not from package
//     defaults. See the comment at that loop for why it matters.
//   - Strip: remove instance-scoped keys from every profile. They are ignored on
//     read from now on, but leaving them means the next reader who forgets the
//     filter finds a value that looks authoritative.
//   - Retire: remove keys no version writes any more, from the instance store
//     and from every profile. See retiredKeys.
//
// Idempotent: a second run promotes nothing (the keys are present) and strips
// nothing (the profiles are clean), so it is safe on every start.
func PromoteInstanceSettings(am *auth.AuthManager) error {
	if am == nil {
		return nil
	}

	instance, err := config.LoadSettingsFile()
	if err != nil {
		return err
	}
	if instance == nil {
		instance = map[string]interface{}{}
	}

	users, err := am.GetAllUsers()
	if err != nil {
		return err
	}

	// The source is the most recently updated ADMIN profile. Not just any
	// profile: a household member's download path must not silently become
	// house policy. Most-recent because with two admins the older one's value
	// is the one they already replaced.
	var src *auth.UserProfile
	for i := range users {
		u := &users[i]
		if !u.IsAdmin || len(u.Settings) == 0 {
			continue
		}
		if src == nil || u.UpdatedAt.After(src.UpdatedAt) {
			src = u
		}
	}

	promoted := 0
	seeded := 0
	if src != nil {
		for _, key := range InstanceKeys() {
			if _, present := instance[key]; present {
				continue // the operator has already set it; never overwrite
			}
			v, ok := src.Settings[key]
			if !ok {
				continue
			}
			instance[key] = v
			promoted++
			slog.Info("[Settings] Promoted to instance scope",
				"key", key, "from_user", src.ID)
		}

		// House defaults for the PERSONAL settings too.
		//
		// The instance layer already applies to every key, not only the
		// instance-scoped ones — that is deliberate, so the operator can set a
		// default that reaches anyone who has not chosen otherwise. Nothing was
		// putting anything there, so a new account fell back to package
		// defaults instead: LOSSLESS where the operator had configured
		// HI_RES_LOSSLESS, and lyrics, genre and provider fallback all off
		// where the operator had them on. A household member would have
		// downloaded worse files than the person who invited them, silently.
		//
		// Seeding from the admin's own values is the closest thing to intent
		// available at migration time. It does not touch their profile: those
		// keys are user-scoped, they stay, and their patch overrides this layer
		// with the same values — so nothing changes for them.
		//
		// The gap this leaves, recorded in issue #73: changing a house default
		// afterwards needs an admin screen that edits the instance store
		// distinctly from the admin's own preferences. Until that exists, these
		// are frozen at the values seeded here.
		for key, v := range src.Settings {
			if ScopeOf(key) == ScopeInstance {
				continue // handled above
			}
			if _, present := instance[key]; present {
				continue
			}
			instance[key] = v
			seeded++
		}
		if seeded > 0 {
			slog.Info("[Settings] Seeded house defaults from the operator's own settings",
				"keys", seeded, "from_user", src.ID)
		}
	}
	retired := 0
	for key := range instance {
		why, dead := RetiredReason(key)
		if !dead {
			continue
		}
		delete(instance, key)
		retired++
		slog.Info("[Settings] Removed a retired instance key", "key", key, "why", why)
	}

	if promoted > 0 || seeded > 0 || retired > 0 {
		if err := config.SaveSettingsFile(instance); err != nil {
			return err
		}
	}

	stripped := 0
	for i := range users {
		u := &users[i]
		if len(u.Settings) == 0 {
			continue
		}
		clean := make(map[string]interface{}, len(u.Settings))
		changed := false
		for k, v := range u.Settings {
			if why, dead := RetiredReason(k); dead {
				changed = true
				slog.Info("[Settings] Removed a retired key from a profile",
					"key", k, "user", u.ID, "why", why)
				continue
			}
			if ScopeOf(k) != ScopeInstance {
				clean[k] = v
				continue
			}
			changed = true
			// Losing a value nobody else holds is the one outcome worth being
			// loud about — it means this account had an instance setting the
			// operator never had, and it is about to stop applying.
			if _, inInstance := instance[k]; !inInstance {
				slog.Warn("[Settings] Dropping an instance-scoped key held only by this user",
					"key", k, "user", u.ID, "value", v)
			}
		}
		if !changed {
			continue
		}
		if err := am.SaveUserSettings(u.ID, clean); err != nil {
			return err
		}
		stripped++
	}

	if promoted > 0 || seeded > 0 || stripped > 0 || retired > 0 {
		slog.Info("[Settings] Scope migration done",
			"promoted", promoted, "house_defaults_seeded", seeded,
			"profiles_stripped", stripped, "retired_instance_keys", retired)
	}
	return nil
}

// HouseDefaults returns the instance store's USER-scoped keys: the values a new
// account starts from before it has chosen anything.
//
// Instance-scoped keys are excluded because they are not defaults — they are the
// value, for everyone, and no account can override them.
func HouseDefaults() (map[string]interface{}, error) {
	instance, err := config.LoadSettingsFile()
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	for k, v := range instance {
		if ScopeOf(k) == ScopeUser {
			out[k] = v
		}
	}
	return out, nil
}

// PublishHouseDefaults makes the caller's own personal settings the starting
// point for accounts that have not chosen otherwise. Returns how many keys were
// written.
//
// This is the migration's seeding step, made repeatable. It exists because
// after that seeding there was no way to change a house default at all: an
// admin's PUT sends user-scoped keys to their own profile, by design, so the
// instance store's copy stayed frozen at whatever the migration found.
//
// It publishes the caller's EFFECTIVE values rather than their stored patch —
// what they see on their own settings screen is what they mean by "my
// settings", and a key they never touched still has a value they are used to.
//
// Deliberately not a second settings form. The alternative was a screen
// duplicating all fifteen personal settings so an operator could set a house
// default different from their own preference, and nobody has wanted that: the
// real gesture is "set the app up the way I like it, then make that the
// starting point for everyone else". If the divergent case ever turns up, that
// editor can be built then, on top of this.
func PublishHouseDefaults(am *auth.AuthManager, userID string) (int, error) {
	if am == nil || userID == "" {
		return 0, nil
	}
	instance, err := config.LoadSettingsFile()
	if err != nil {
		return 0, err
	}
	if instance == nil {
		instance = map[string]interface{}{}
	}

	mine := EffectiveBlob(am, userID)
	written := 0
	for k, v := range mine {
		if ScopeOf(k) != ScopeUser {
			continue
		}
		if SameValue(instance[k], v) {
			continue
		}
		instance[k] = v
		written++
	}
	if written == 0 {
		return 0, nil
	}
	if err := config.SaveSettingsFile(instance); err != nil {
		return 0, err
	}
	slog.Info("[Settings] House defaults published", "keys", written, "from_user", userID)
	return written, nil
}
