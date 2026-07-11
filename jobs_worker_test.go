package main

import "testing"

// TestProcessJobSafelyRecoversPanicAndMarksJobFailed is the regression test
// for Q1 as it applies specifically to the job worker: jobWorkers is 1 by
// default, so an unrecovered panic while processing one job wouldn't just
// crash the process — if only the top-level worker goroutine were
// protected, it would permanently kill the sole worker and silently stall
// the entire download queue forever (see processJobSafely's doc comment).
// getWatchlistSettings is an injectable seam in the real processJob code
// path (not a test double bolted on the side), so this exercises the real
// panic-to-recovery flow end to end rather than testing processJobSafely's
// recover block in isolation.
func TestProcessJobSafelyRecoversPanicAndMarksJobFailed(t *testing.T) {
	jm := newTestJobManager(t, false)
	jm.getWatchlistSettings = func(watchlistID string) (JobSettings, bool) {
		panic("boom")
	}

	job := &Job{ID: "job-panic-test", SpotifyID: "x", WatchlistID: "wl-1", Status: StatusPending}
	if err := jm.saveJob(job); err != nil {
		t.Fatalf("saveJob: %v", err)
	}

	// Must not panic/crash the test process.
	jm.processJobSafely("job-panic-test")

	got, err := jm.GetJob("job-panic-test")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", got.Status, StatusFailed)
	}
	if got.Error == "" {
		t.Error("Error is empty, want a recorded panic message")
	}
}

// TestProcessJobSafelyLeavesWorkerLoopUsable proves the worker loop itself
// survives a panicking job and can go on to process the next one — the
// actual point of recovering per job instead of around the whole worker()
// goroutine.
func TestProcessJobSafelyLeavesWorkerLoopUsable(t *testing.T) {
	jm := newTestJobManager(t, false)
	panicNext := true
	jm.getWatchlistSettings = func(watchlistID string) (JobSettings, bool) {
		if panicNext {
			panicNext = false
			panic("boom")
		}
		return JobSettings{}, false
	}

	bad := &Job{ID: "job-bad", SpotifyID: "x", WatchlistID: "wl-1", Status: StatusPending}
	good := &Job{ID: "job-good", SpotifyID: "y", WatchlistID: "wl-1", Status: StatusPending}
	if err := jm.saveJob(bad); err != nil {
		t.Fatalf("saveJob(bad): %v", err)
	}
	if err := jm.saveJob(good); err != nil {
		t.Fatalf("saveJob(good): %v", err)
	}

	jm.processJobSafely("job-bad")
	jm.processJobSafely("job-good")

	gotBad, err := jm.GetJob("job-bad")
	if err != nil {
		t.Fatalf("GetJob(job-bad): %v", err)
	}
	if gotBad.Status != StatusFailed {
		t.Errorf("job-bad Status = %q, want %q", gotBad.Status, StatusFailed)
	}

	gotGood, err := jm.GetJob("job-good")
	if err != nil {
		t.Fatalf("GetJob(job-good): %v", err)
	}
	// job-good has no SpotifyID/ServiceURL that resolves to anything
	// downloadable in this test environment, so it's expected to fail for
	// an ordinary reason (no network access to a real provider) — the
	// point being asserted is that it was actually *attempted* (its status
	// moved off Pending) rather than the worker loop having died after
	// job-bad's panic.
	if gotGood.Status == StatusPending {
		t.Error("job-good was never processed — the panic from job-bad should not have blocked it")
	}
}
