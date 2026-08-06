package main

import (
	"context"
	"github.com/sos-pc/SpotiFLAC-SH/internal/jobs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestV1JobsStreamInitialSnapshotScopedToUser is the end-to-end regression
// test for the cross-user leak: v1JobsStream's initial snapshot (sent right
// after a client connects, read straight from BoltDB) must only include the
// requesting user's own jobs, mirroring the same per-event filter already
// applied to live job_update pushes further down in the same handler.
func TestV1JobsStreamInitialSnapshotScopedToUser(t *testing.T) {
	jm, hub := newTestJobManagerWithHub(t, false)
	s := &Server{ctr: &Container{Jobs: jm, SSE: hub}}

	userAJob := &jobs.Job{ID: "job-a", SpotifyID: "track-a", TrackName: "Song A", UserID: "user-a", Status: jobs.StatusDone, UpdatedAt: time.Now()}
	userBJob := &jobs.Job{ID: "job-b", SpotifyID: "track-b", TrackName: "Song B", UserID: "user-b", Status: jobs.StatusDone, UpdatedAt: time.Now()}
	if err := jm.SaveJob(userAJob); err != nil {
		t.Fatalf("SaveJob(userAJob): %v", err)
	}
	if err := jm.SaveJob(userBJob); err != nil {
		t.Fatalf("SaveJob(userBJob): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	claims := &JWTClaims{UserID: "user-a", IsAdmin: false}
	ctx = context.WithValue(ctx, contextKeyUser, claims)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.v1JobsStream(w, r)
		close(done)
	}()

	// Give the handler time to write its initial snapshot + "connected"
	// event before cancelling the request context to end the stream.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("v1JobsStream did not return after context cancellation")
	}

	body := w.Body.String()
	if !strings.Contains(body, "job-a") {
		t.Error("snapshot should include the requesting user's own job (job-a)")
	}
	if strings.Contains(body, "job-b") {
		t.Error("snapshot leaked another user's job (job-b) into the stream")
	}
}

// TestV1JobsStreamSendsHeartbeatWhileIdle covers the keepalive: with no job
// producing events, the handler must still write something periodically, or a
// reverse proxy closes the silent upstream (nginx does it at
// proxy_read_timeout — 240s in SWAG's default proxy.conf) and every idle
// client reconnects on a loop, re-sending the whole 48h snapshot each time.
//
// The keepalive must be an SSE *comment*: clients ignore those entirely, so it
// cannot reach an event handler and be mistaken for a job update.
func TestV1JobsStreamSendsHeartbeatWhileIdle(t *testing.T) {
	orig := sseHeartbeatInterval
	sseHeartbeatInterval = 10 * time.Millisecond
	defer func() { sseHeartbeatInterval = orig }()

	jm, hub := newTestJobManagerWithHub(t, false)
	s := &Server{ctr: &Container{Jobs: jm, SSE: hub}}

	ctx, cancel := context.WithCancel(context.Background())
	claims := &JWTClaims{UserID: "user-a", IsAdmin: false}
	ctx = context.WithValue(ctx, contextKeyUser, claims)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.v1JobsStream(w, r)
		close(done)
	}()

	// Long enough for several ticks, with nothing ever published to the hub —
	// the idle case the heartbeat exists for.
	time.Sleep(120 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("v1JobsStream did not return after context cancellation")
	}

	body := w.Body.String()
	if !strings.Contains(body, ": keepalive") {
		t.Errorf("idle stream sent no heartbeat; a proxy would cut it. body=%q", body)
	}
	// A comment line, not an event: "event: keepalive" would surface client-side.
	if strings.Contains(body, "event: keepalive") {
		t.Error("heartbeat must be an SSE comment, not an event clients can handle")
	}
}

// TestV1JobsStreamInitialSnapshotAdminSeesEveryone verifies the admin
// bypass on the read side, mirroring the same bypass already covered for
// deletion in TestClearCompletedJobsAdminClearsEveryone.
func TestV1JobsStreamInitialSnapshotAdminSeesEveryone(t *testing.T) {
	jm, hub := newTestJobManagerWithHub(t, false)
	s := &Server{ctr: &Container{Jobs: jm, SSE: hub}}

	userAJob := &jobs.Job{ID: "job-a", SpotifyID: "track-a", UserID: "user-a", Status: jobs.StatusDone, UpdatedAt: time.Now()}
	userBJob := &jobs.Job{ID: "job-b", SpotifyID: "track-b", UserID: "user-b", Status: jobs.StatusDone, UpdatedAt: time.Now()}
	if err := jm.SaveJob(userAJob); err != nil {
		t.Fatalf("SaveJob(userAJob): %v", err)
	}
	if err := jm.SaveJob(userBJob); err != nil {
		t.Fatalf("SaveJob(userBJob): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	claims := &JWTClaims{UserID: "admin-1", IsAdmin: true}
	ctx = context.WithValue(ctx, contextKeyUser, claims)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.v1JobsStream(w, r)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("v1JobsStream did not return after context cancellation")
	}

	body := w.Body.String()
	if !strings.Contains(body, "job-a") || !strings.Contains(body, "job-b") {
		t.Errorf("admin snapshot should include every user's jobs, got: %s", body)
	}
}

// TestV1JobsStreamSnapshotReflectsPersistedProgress is the regression test
// for the reconnect-stats bug: a client that (re)connects mid-download —
// page refresh, tab back from the background, a brief network drop — reads
// its initial snapshot straight from this same BoltDB record. Before the
// throttled-persist fix in jobs_worker.go's SpeedCallback, Speed/TotalSize
// were only ever saved before any bytes moved (0) and after the download
// finished, so a reconnecting client saw zeros ("—" in the UI) for the
// entire remaining download. This confirms that once progress values are
// persisted (as the throttled save now does periodically), a fresh
// connection's snapshot reflects them instead of defaulting to zero.
func TestV1JobsStreamSnapshotReflectsPersistedProgress(t *testing.T) {
	jm, hub := newTestJobManagerWithHub(t, false)
	s := &Server{ctr: &Container{Jobs: jm, SSE: hub}}

	job := &jobs.Job{
		ID: "job-a", SpotifyID: "track-a", UserID: "user-a",
		Status: jobs.StatusDownloading, Speed: 2.5, TotalSize: 10.3,
		UpdatedAt: time.Now(),
	}
	if err := jm.SaveJob(job); err != nil {
		t.Fatalf("SaveJob: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	claims := &JWTClaims{UserID: "user-a", IsAdmin: false}
	ctx = context.WithValue(ctx, contextKeyUser, claims)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		s.v1JobsStream(w, r)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("v1JobsStream did not return after context cancellation")
	}

	body := w.Body.String()
	if !strings.Contains(body, `"speed":2.5`) {
		t.Errorf("snapshot should reflect the persisted in-progress speed, got: %s", body)
	}
	if !strings.Contains(body, `"total_size":10.3`) {
		t.Errorf("snapshot should reflect the persisted in-progress size, got: %s", body)
	}
}
