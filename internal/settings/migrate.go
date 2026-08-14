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
	if promoted > 0 || seeded > 0 {
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

	if promoted > 0 || seeded > 0 || stripped > 0 {
		slog.Info("[Settings] Scope migration done",
			"promoted", promoted, "house_defaults_seeded", seeded, "profiles_stripped", stripped)
	}
	return nil
}
