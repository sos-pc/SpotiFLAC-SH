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
// Two halves, in this order, and both are needed:
//
//   - Promote: for each instance-scoped key missing from the instance store but
//     present in an admin's profile, write it to the instance store.
//   - Strip: remove instance-scoped keys from every profile. They are ignored on
//     read from now on, but leaving them means the next reader who forgets the
//     filter finds a value that looks authoritative.
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
	}
	if promoted > 0 {
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

	if promoted > 0 || stripped > 0 {
		slog.Info("[Settings] Scope migration done",
			"promoted", promoted, "profiles_stripped", stripped)
	}
	return nil
}
