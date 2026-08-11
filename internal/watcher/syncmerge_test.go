package watcher

import (
	"testing"
	"time"
)

// TestApplySyncResultKeepsConcurrentChanges is the reason syncPlaylist re-reads
// the record instead of saving the snapshot it started from.
//
// A sync copies the watchlist, fetches Spotify, enqueues a batch and only then
// saves — hours later on a large playlist with one job worker. Everything the
// user changed in that window is newer than the copy. Saving the copy whole
// reverted it, which is how `tout.m3u8` came back on 2026-08-10: a rename had
// moved the file and recorded the new name, and the sync running at the time
// wrote the old name back over it.
func TestApplySyncResultKeepsConcurrentChanges(t *testing.T) {
	// What the sync started from.
	snapshot := &WatchedPlaylist{
		ID:            "watch-1",
		Name:          "/all",
		TrackIDs:      []string{"a", "b"},
		M3U8File:      "tout.m3u8",
		IntervalHours: 24,
	}

	// What the user did while it ran: renamed the playlist, which decided a new
	// filename, and changed two settings.
	fresh := &WatchedPlaylist{
		ID:            "watch-1",
		Name:          "/all",
		TrackIDs:      []string{"a", "b"},
		M3U8File:      "all [methammer].m3u8",
		CustomName:    "/all [methammer]",
		IntervalHours: 6,
		SyncDeletions: true,
	}

	applySyncResult(fresh, snapshot, []string{"c"}, SyncLog{NewTracks: 1})

	if fresh.M3U8File != "all [methammer].m3u8" {
		t.Errorf("M3U8File = %q, want the renamed file — the sync reverted it and the old name will be rewritten", fresh.M3U8File)
	}
	if fresh.CustomName != "/all [methammer]" {
		t.Errorf("CustomName = %q, want the name the user typed", fresh.CustomName)
	}
	if fresh.IntervalHours != 6 {
		t.Errorf("IntervalHours = %d, want 6 — the snapshot's 24 was reverted onto it", fresh.IntervalHours)
	}
	if !fresh.SyncDeletions {
		t.Error("SyncDeletions = false, want the value set during the sync")
	}

	// And the fields the sync does own still land.
	if len(fresh.TrackIDs) != 3 || fresh.TrackIDs[2] != "c" {
		t.Errorf("TrackIDs = %v, want the snapshot's list plus the enqueued track", fresh.TrackIDs)
	}
	if fresh.LastSync.IsZero() {
		t.Error("LastSync not stamped")
	}
}

// TestApplySyncResultKeepsBatchCounters: OnBatchComplete writes download counts
// into an existing SyncLog entry while the sync that created it is still
// draining. Appending the new entry to the snapshot's list instead of the
// stored one carried those counts away — the loss OnBatchComplete's
// "standalone entry" fallback was written to paper over.
func TestApplySyncResultKeepsBatchCounters(t *testing.T) {
	snapshot := &WatchedPlaylist{
		ID:       "watch-1",
		TrackIDs: []string{"a"},
		// The entry as it looked when the sync copied the record: no counts yet.
		SyncLogs: []SyncLog{{BatchID: "batch-1"}},
	}
	fresh := &WatchedPlaylist{
		ID:       "watch-1",
		TrackIDs: []string{"a"},
		// The same entry after OnBatchComplete filled it in.
		SyncLogs: []SyncLog{{BatchID: "batch-1", Downloaded: 12, Failed: 1}},
	}

	applySyncResult(fresh, snapshot, nil, SyncLog{BatchID: "batch-2"})

	if len(fresh.SyncLogs) != 2 {
		t.Fatalf("SyncLogs has %d entries, want the recorded one plus the new one", len(fresh.SyncLogs))
	}
	if fresh.SyncLogs[0].Downloaded != 12 || fresh.SyncLogs[0].Failed != 1 {
		t.Errorf("batch-1 counters = %d/%d, want 12/1 — OnBatchComplete's write was reverted",
			fresh.SyncLogs[0].Downloaded, fresh.SyncLogs[0].Failed)
	}
	if fresh.SyncLogs[1].BatchID != "batch-2" {
		t.Errorf("newest entry = %q, want batch-2", fresh.SyncLogs[1].BatchID)
	}
}

// TestApplySyncResultCapsHistory: the cap trims the oldest, and trims the list
// that is actually stored rather than the snapshot's copy of it.
func TestApplySyncResultCapsHistory(t *testing.T) {
	fresh := &WatchedPlaylist{ID: "watch-1"}
	for i := 0; i < maxSyncLogs; i++ {
		fresh.SyncLogs = append(fresh.SyncLogs, SyncLog{BatchID: "old", Time: time.Unix(int64(i), 0)})
	}
	snapshot := &WatchedPlaylist{ID: "watch-1"}

	applySyncResult(fresh, snapshot, nil, SyncLog{BatchID: "newest"})

	if len(fresh.SyncLogs) != maxSyncLogs {
		t.Errorf("SyncLogs = %d entries, want the cap of %d", len(fresh.SyncLogs), maxSyncLogs)
	}
	if fresh.SyncLogs[len(fresh.SyncLogs)-1].BatchID != "newest" {
		t.Error("the new entry was trimmed away instead of the oldest")
	}
}

// TestApplySyncResultAliasSafe: syncPlaylist passes two distinct records today,
// but the function must not corrupt anything if ever handed the same one twice —
// it writes every field it touches before reading it.
func TestApplySyncResultAliasSafe(t *testing.T) {
	pl := &WatchedPlaylist{ID: "watch-1", Name: "Release Radar", TrackIDs: []string{"a"}}

	applySyncResult(pl, pl, []string{"b"}, SyncLog{BatchID: "batch-1"})

	if len(pl.TrackIDs) != 2 || pl.TrackIDs[1] != "b" {
		t.Errorf("TrackIDs = %v, want [a b]", pl.TrackIDs)
	}
	if len(pl.SyncLogs) != 1 {
		t.Errorf("SyncLogs = %d entries, want 1", len(pl.SyncLogs))
	}
}
