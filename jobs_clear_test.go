package main

import "testing"

// TestClearCompletedJobsScopedToUser is the regression test for the
// cross-user leak: clearing completed downloads used to delete every
// user's done/skipped jobs, not just the caller's own.
func TestClearCompletedJobsScopedToUser(t *testing.T) {
	jm := newTestJobManager(t, false)

	userAJob := &Job{ID: "job-a", SpotifyID: "track-a", UserID: "user-a", Status: StatusDone}
	userBJob := &Job{ID: "job-b", SpotifyID: "track-b", UserID: "user-b", Status: StatusDone}
	if err := jm.saveJob(userAJob); err != nil {
		t.Fatalf("saveJob(userAJob): %v", err)
	}
	if err := jm.saveJob(userBJob); err != nil {
		t.Fatalf("saveJob(userBJob): %v", err)
	}

	deleted, err := jm.ClearCompletedJobs("user-a", false)
	if err != nil {
		t.Fatalf("ClearCompletedJobs: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "job-a" {
		t.Fatalf("deleted = %v, want [job-a]", deleted)
	}

	if _, err := jm.GetJob("job-a"); err == nil {
		t.Error("job-a should have been deleted")
	}
	if _, err := jm.GetJob("job-b"); err != nil {
		t.Errorf("job-b (another user's job) should still exist: %v", err)
	}
}

// TestClearCompletedJobsAdminClearsEveryone verifies the intended admin
// bypass: an admin's "clear completed" affects every user's jobs, not just
// their own — this is the one case where cross-user deletion is correct.
func TestClearCompletedJobsAdminClearsEveryone(t *testing.T) {
	jm := newTestJobManager(t, false)

	userAJob := &Job{ID: "job-a", SpotifyID: "track-a", UserID: "user-a", Status: StatusDone}
	userBJob := &Job{ID: "job-b", SpotifyID: "track-b", UserID: "user-b", Status: StatusDone}
	if err := jm.saveJob(userAJob); err != nil {
		t.Fatalf("saveJob(userAJob): %v", err)
	}
	if err := jm.saveJob(userBJob); err != nil {
		t.Fatalf("saveJob(userBJob): %v", err)
	}

	deleted, err := jm.ClearCompletedJobs("user-a", true)
	if err != nil {
		t.Fatalf("ClearCompletedJobs: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want 2 jobs (admin clears everyone)", deleted)
	}
	if _, err := jm.GetJob("job-a"); err == nil {
		t.Error("job-a should have been deleted")
	}
	if _, err := jm.GetJob("job-b"); err == nil {
		t.Error("job-b should have been deleted (admin bypass)")
	}
}

// TestClearCompletedJobsKeepsWatchlistSkips verifies the pre-existing
// exclusion (skipped jobs tied to a watchlist must survive, or the next
// sync would immediately re-enqueue the same track) still holds with the
// new per-user scoping.
func TestClearCompletedJobsKeepsWatchlistSkips(t *testing.T) {
	jm := newTestJobManager(t, false)

	manualSkip := &Job{ID: "job-manual", SpotifyID: "track-1", UserID: "user-a", Status: StatusSkipped}
	watchlistSkip := &Job{ID: "job-watchlist", SpotifyID: "track-2", UserID: "user-a", WatchlistID: "wl-1", Status: StatusSkipped}
	if err := jm.saveJob(manualSkip); err != nil {
		t.Fatalf("saveJob(manualSkip): %v", err)
	}
	if err := jm.saveJob(watchlistSkip); err != nil {
		t.Fatalf("saveJob(watchlistSkip): %v", err)
	}

	deleted, err := jm.ClearCompletedJobs("user-a", false)
	if err != nil {
		t.Fatalf("ClearCompletedJobs: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "job-manual" {
		t.Fatalf("deleted = %v, want [job-manual]", deleted)
	}
	if _, err := jm.GetJob("job-watchlist"); err != nil {
		t.Errorf("watchlist-tied skip should survive: %v", err)
	}
}

// TestClearAllJobsScopedToUser mirrors the completed-jobs case for the
// "clear all" endpoint.
func TestClearAllJobsScopedToUser(t *testing.T) {
	jm := newTestJobManager(t, false)

	userAJob := &Job{ID: "job-a", SpotifyID: "track-a", UserID: "user-a", Status: StatusFailed}
	userBJob := &Job{ID: "job-b", SpotifyID: "track-b", UserID: "user-b", Status: StatusFailed}
	if err := jm.saveJob(userAJob); err != nil {
		t.Fatalf("saveJob(userAJob): %v", err)
	}
	if err := jm.saveJob(userBJob); err != nil {
		t.Fatalf("saveJob(userBJob): %v", err)
	}

	deleted, err := jm.ClearAllJobs("user-a", false)
	if err != nil {
		t.Fatalf("ClearAllJobs: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "job-a" {
		t.Fatalf("deleted = %v, want [job-a]", deleted)
	}
	if _, err := jm.GetJob("job-b"); err != nil {
		t.Errorf("job-b (another user's job) should still exist: %v", err)
	}
}

// TestClearAllJobsLeavesActiveDownloadsAlone is the regression test for the
// UX mismatch found during the audit: the frontend already assumes active
// downloads survive a "Clear All" click (it only removes terminal jobs from
// its local view), but the backend used to delete literally every job,
// pending/downloading included.
func TestClearAllJobsLeavesActiveDownloadsAlone(t *testing.T) {
	jm := newTestJobManager(t, false)

	pending := &Job{ID: "job-pending", SpotifyID: "track-1", UserID: "user-a", Status: StatusPending}
	downloading := &Job{ID: "job-downloading", SpotifyID: "track-2", UserID: "user-a", Status: StatusDownloading}
	done := &Job{ID: "job-done", SpotifyID: "track-3", UserID: "user-a", Status: StatusDone}
	for _, j := range []*Job{pending, downloading, done} {
		if err := jm.saveJob(j); err != nil {
			t.Fatalf("saveJob(%s): %v", j.ID, err)
		}
	}

	deleted, err := jm.ClearAllJobs("user-a", false)
	if err != nil {
		t.Fatalf("ClearAllJobs: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != "job-done" {
		t.Fatalf("deleted = %v, want [job-done]", deleted)
	}
	if _, err := jm.GetJob("job-pending"); err != nil {
		t.Errorf("pending job should survive Clear All: %v", err)
	}
	if _, err := jm.GetJob("job-downloading"); err != nil {
		t.Errorf("downloading job should survive Clear All: %v", err)
	}
}

// TestClearJobsBroadcastsOwnerScopedEvents verifies the SSE side of the
// leak fix: a job_deleted event must carry the deleted job's real UserID,
// not a blank stub, so v1JobsStream's existing per-user filter can keep the
// broadcast scoped instead of notifying every connected client.
func TestClearJobsBroadcastsOwnerScopedEvents(t *testing.T) {
	jm := newTestJobManager(t, false)

	job := &Job{ID: "job-a", SpotifyID: "track-a", UserID: "user-a", Status: StatusDone}
	if err := jm.saveJob(job); err != nil {
		t.Fatalf("saveJob: %v", err)
	}

	ch := jm.hub.subscribe()
	defer jm.hub.unsubscribe(ch)

	if _, err := jm.ClearCompletedJobs("user-a", false); err != nil {
		t.Fatalf("ClearCompletedJobs: %v", err)
	}

	select {
	case event := <-ch:
		if event.Type != "job_deleted" {
			t.Fatalf("event.Type = %q, want job_deleted", event.Type)
		}
		if event.Job == nil || event.Job.UserID != "user-a" {
			t.Fatalf("event.Job = %+v, want UserID user-a", event.Job)
		}
	default:
		t.Fatal("expected a job_deleted event, got none")
	}
}
