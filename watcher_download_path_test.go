package main

import (
	"testing"

	"github.com/afkarxyz/SpotiFLAC/backend/util"
)

// TestEffectiveDownloadPath covers the R7 helper that replaced five copies of
// the "watchlist DownloadPath, or the default music dir if unset" fallback.
func TestEffectiveDownloadPath(t *testing.T) {
	configured := &WatchedPlaylist{Settings: JobSettings{DownloadPath: "/music/lib"}}
	if got := configured.EffectiveDownloadPath(); got != "/music/lib" {
		t.Errorf("configured path = %q, want /music/lib", got)
	}

	unset := &WatchedPlaylist{}
	if got := unset.EffectiveDownloadPath(); got != util.GetDefaultMusicPath() {
		t.Errorf("unset path = %q, want default %q", got, util.GetDefaultMusicPath())
	}
}
