package util

import (
	"os"
	"path/filepath"
)

// Default values for settings that several layers each have to substitute when
// their own input arrives empty.
//
// They live here, and not in internal/settings where the settings types are,
// because internal/settings imports internal/jobs (ServerJobSettings returns a
// jobs.JobSettings) — so internal/jobs cannot import it back. backend/util is
// already imported by every layer that needs these, which is what makes it the
// one place all three can agree on.
//
// Declaring them is the point. "title-artist" was written out at three call
// sites and "tidal" at two, in packages that never see each other, so the
// answer to "what is the default filename template" depended on which file you
// happened to open. Changing it meant finding all of them.
//
// This does NOT mean the substitutions are redundant with each other — see the
// comment at each site. They guard different inputs: a persisted job's stored
// settings, a downloader request built by an internal caller, and freshly
// resolved settings. Same value, different reasons.
const (
	DefaultFilenameTemplate = "title-artist"
	DefaultService          = "tidal"
	DefaultAudioFormat      = "LOSSLESS"
)

func GetDefaultMusicPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "/music"
	}
	return filepath.Join(homeDir, "Music")
}
