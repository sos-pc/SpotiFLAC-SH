package watcher

import "testing"

func TestComputeFreshnessReportFullyUpToDate(t *testing.T) {
	report := computeFreshnessReport(
		[]string{"a", "b", "c"}, []string{"a", "b", "c"},
		3,    // resolvedCount == len(local)
		0, 0, // pending, failed
		true, 3, true, // m3u8 enabled, 3 entries, exists
	)
	if !report.UpToDate {
		t.Errorf("UpToDate = false, want true: %+v", report)
	}
	if report.NewOnSpotify != 0 || report.RemovedFromSpotify != 0 || report.MissingFiles != 0 || report.M3U8Stale {
		t.Errorf("expected all-zero diffs, got %+v", report)
	}
}

func TestComputeFreshnessReportNewTrackOnSpotify(t *testing.T) {
	report := computeFreshnessReport(
		[]string{"a", "b"}, []string{"a", "b", "c"}, // "c" is new on Spotify
		2, 0, 0, false, 0, false,
	)
	if report.UpToDate {
		t.Error("UpToDate = true, want false: a new Spotify track is untracked locally")
	}
	if report.NewOnSpotify != 1 {
		t.Errorf("NewOnSpotify = %d, want 1", report.NewOnSpotify)
	}
	if report.RemovedFromSpotify != 0 {
		t.Errorf("RemovedFromSpotify = %d, want 0", report.RemovedFromSpotify)
	}
}

func TestComputeFreshnessReportRemovedFromSpotify(t *testing.T) {
	report := computeFreshnessReport(
		[]string{"a", "b", "c"}, []string{"a", "b"}, // "c" no longer on Spotify
		3, 0, 0, false, 0, false,
	)
	if report.UpToDate {
		t.Error("UpToDate = true, want false: a local track left the Spotify playlist")
	}
	if report.RemovedFromSpotify != 1 {
		t.Errorf("RemovedFromSpotify = %d, want 1", report.RemovedFromSpotify)
	}
}

func TestComputeFreshnessReportMissingFile(t *testing.T) {
	// 3 tracks locally, but only 2 resolved to a real, stat-verified file —
	// the third is claimed but not actually present on disk.
	report := computeFreshnessReport(
		[]string{"a", "b", "c"}, []string{"a", "b", "c"},
		2, 0, 0, false, 0, false,
	)
	if report.UpToDate {
		t.Error("UpToDate = true, want false: a track has no verified file")
	}
	if report.MissingFiles != 1 {
		t.Errorf("MissingFiles = %d, want 1", report.MissingFiles)
	}
}

func TestComputeFreshnessReportPendingOrFailedBlocksUpToDate(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		report := computeFreshnessReport([]string{"a"}, []string{"a"}, 1, 1, 0, false, 0, false)
		if report.UpToDate {
			t.Error("UpToDate = true, want false: a track is still pending")
		}
	})
	t.Run("failed", func(t *testing.T) {
		report := computeFreshnessReport([]string{"a"}, []string{"a"}, 1, 0, 1, false, 0, false)
		if report.UpToDate {
			t.Error("UpToDate = true, want false: a track has failed")
		}
	})
}

func TestComputeFreshnessReportM3U8Stale(t *testing.T) {
	t.Run("fewer entries than resolved", func(t *testing.T) {
		// 3 tracks resolve to real files, but the M3U8 on disk only has 2 —
		// e.g. the shrink-guard blocked a regen, or a write failed partway.
		report := computeFreshnessReport(
			[]string{"a", "b", "c"}, []string{"a", "b", "c"},
			3, 0, 0, true, 2, true,
		)
		if report.UpToDate {
			t.Error("UpToDate = true, want false: M3U8 is behind what's resolvable")
		}
		if !report.M3U8Stale {
			t.Error("M3U8Stale = false, want true")
		}
	})

	t.Run("missing entirely despite resolvable tracks", func(t *testing.T) {
		report := computeFreshnessReport(
			[]string{"a"}, []string{"a"},
			1, 0, 0, true, 0, false, // m3u8Exists=false
		)
		if !report.M3U8Stale {
			t.Error("M3U8Stale = false, want true: no M3U8 file at all despite a resolvable track")
		}
	})

	t.Run("disabled entirely does not count as stale", func(t *testing.T) {
		report := computeFreshnessReport(
			[]string{"a"}, []string{"a"},
			1, 0, 0, false, 0, false, // m3u8Enabled=false
		)
		if report.M3U8Stale {
			t.Error("M3U8Stale = true, want false: M3U8 generation is disabled for this watchlist")
		}
		if !report.UpToDate {
			t.Error("UpToDate = false, want true: nothing else is wrong")
		}
	})

	t.Run("no resolved tracks and no M3U8 is not stale", func(t *testing.T) {
		// An empty/never-synced watchlist shouldn't be flagged stale just
		// because there's no M3U8 yet — there's nothing to put in one.
		report := computeFreshnessReport(
			nil, nil,
			0, 0, 0, true, 0, false,
		)
		if report.M3U8Stale {
			t.Error("M3U8Stale = true, want false: no tracks resolved, nothing to write")
		}
	})
}
