package main

import "testing"

func TestUpdateJobFilePathsForRenameUpdatesMatchingJob(t *testing.T) {
	jm := newTestJobManager(t, false)

	target := &Job{ID: "job-target", SpotifyID: "renamed-track", FilePath: "/music/old.flac", Status: StatusDone}
	other := &Job{ID: "job-other", SpotifyID: "other-track", FilePath: "/music/other.flac", Status: StatusDone}
	if err := jm.saveJob(target); err != nil {
		t.Fatalf("saveJob(target): %v", err)
	}
	if err := jm.saveJob(other); err != nil {
		t.Fatalf("saveJob(other): %v", err)
	}

	updated, err := jm.UpdateJobFilePathsForRename("/music/old.flac", "/music/new.flac")
	if err != nil {
		t.Fatalf("UpdateJobFilePathsForRename: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}

	got, err := jm.GetJob("job-target")
	if err != nil {
		t.Fatalf("GetJob(job-target): %v", err)
	}
	if got.FilePath != "/music/new.flac" {
		t.Errorf("job-target.FilePath = %q, want /music/new.flac", got.FilePath)
	}

	untouched, err := jm.GetJob("job-other")
	if err != nil {
		t.Fatalf("GetJob(job-other): %v", err)
	}
	if untouched.FilePath != "/music/other.flac" {
		t.Errorf("job-other.FilePath was touched: %q", untouched.FilePath)
	}
}

func TestUpdateJobFilePathsForRenameNoMatchIsNoop(t *testing.T) {
	jm := newTestJobManager(t, false)

	job := &Job{ID: "job-1", SpotifyID: "x", FilePath: "/music/a.flac", Status: StatusDone}
	if err := jm.saveJob(job); err != nil {
		t.Fatalf("saveJob: %v", err)
	}

	updated, err := jm.UpdateJobFilePathsForRename("/music/nonexistent.flac", "/music/new.flac")
	if err != nil {
		t.Fatalf("UpdateJobFilePathsForRename: %v", err)
	}
	if updated != 0 {
		t.Fatalf("updated = %d, want 0", updated)
	}

	got, err := jm.GetJob("job-1")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.FilePath != "/music/a.flac" {
		t.Errorf("job-1.FilePath was touched: %q", got.FilePath)
	}
}

// TestUpdateJobFilePathsForRenameUpdatesMultipleMatches covers the (rare
// but possible) case of more than one job row sharing the same FilePath —
// all of them must be updated, not just the first one found.
func TestUpdateJobFilePathsForRenameUpdatesMultipleMatches(t *testing.T) {
	jm := newTestJobManager(t, false)

	a := &Job{ID: "job-a", SpotifyID: "track-a", FilePath: "/music/shared.flac", Status: StatusDone}
	b := &Job{ID: "job-b", SpotifyID: "track-b", FilePath: "/music/shared.flac", Status: StatusDone}
	if err := jm.saveJob(a); err != nil {
		t.Fatalf("saveJob(a): %v", err)
	}
	if err := jm.saveJob(b); err != nil {
		t.Fatalf("saveJob(b): %v", err)
	}

	updated, err := jm.UpdateJobFilePathsForRename("/music/shared.flac", "/music/renamed.flac")
	if err != nil {
		t.Fatalf("UpdateJobFilePathsForRename: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
}
