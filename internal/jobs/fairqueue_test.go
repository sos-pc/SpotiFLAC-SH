package jobs

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func drain(t *testing.T, q *fairQueue, n int) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, ok := q.take(ctx)
		if !ok {
			t.Fatalf("take returned false after %d of %d", i, n)
		}
		got = append(got, id)
	}
	return got
}

// The complaint this exists to answer: one person's 2561-track sync used to
// hold the only worker while everyone else's single track waited behind all of
// it. Scaled down, but the shape is the one that matters — the three latecomers
// must not be last.
func TestBigBatchDoesNotStarveOthers(t *testing.T) {
	q := newFairQueue()

	for i := 0; i < 50; i++ {
		q.push("bulk-user", "bulk-"+strconv.Itoa(i))
	}
	q.push("alice", "alice-1")
	q.push("bob", "bob-1")
	q.push("carol", "carol-1")

	// Six picks is enough to see it: FIFO would hand out six bulk jobs.
	got := drain(t, q, 6)

	seen := map[string]bool{}
	for _, id := range got {
		seen[id] = true
	}
	for _, want := range []string{"alice-1", "bob-1", "carol-1"} {
		if !seen[want] {
			t.Errorf("%s did not appear in the first six picks (%v) — a single batch is still monopolising the worker", want, got)
		}
	}
}

// Within one user, order is preserved. Fairness is between people; nobody's own
// downloads should come back shuffled.
func TestOrderIsPreservedWithinAUser(t *testing.T) {
	q := newFairQueue()
	for i := 0; i < 4; i++ {
		q.push("solo", "job-"+strconv.Itoa(i))
	}

	got := drain(t, q, 4)
	for i, id := range got {
		if want := "job-" + strconv.Itoa(i); id != want {
			t.Errorf("pick %d = %q, want %q — a user's own order was not kept", i, id, want)
		}
	}
}

// Strict alternation with equal-sized queues, which is the clearest statement
// of what round-robin means here.
func TestAlternatesBetweenUsers(t *testing.T) {
	q := newFairQueue()
	q.push("a", "a1")
	q.push("a", "a2")
	q.push("b", "b1")
	q.push("b", "b2")

	got := drain(t, q, 4)
	owners := ""
	for _, id := range got {
		owners += string(id[0])
	}
	if owners != "abab" {
		t.Errorf("owner sequence = %q, want %q (got %v)", owners, "abab", got)
	}
}

// A user who runs out mid-cycle must be dropped without disturbing the rest.
func TestUserRunningOutIsRemovedCleanly(t *testing.T) {
	q := newFairQueue()
	q.push("a", "a1")
	q.push("b", "b1")
	q.push("b", "b2")
	q.push("b", "b3")

	got := drain(t, q, 4)
	if len(got) != 4 {
		t.Fatalf("got %d jobs, want 4", len(got))
	}
	if got[0] != "a1" {
		t.Errorf("first pick = %q, want a1", got[0])
	}
	// Everything else is b's, in order, and nothing is lost or repeated.
	rest := got[1:]
	for i, want := range []string{"b1", "b2", "b3"} {
		if rest[i] != want {
			t.Errorf("pick %d = %q, want %q (all: %v)", i+1, rest[i], want, got)
		}
	}
	if jobs, owners := q.pending(); jobs != 0 || owners != 0 {
		t.Errorf("queue not empty after draining: %d jobs, %d owners", jobs, owners)
	}
}

// Jobs predating authentication carry no UserID. They form their own bucket
// rather than being attributed to whoever happens to be first.
func TestOwnerlessJobsAreTheirOwnBucket(t *testing.T) {
	q := newFairQueue()
	q.push("", "legacy-1")
	q.push("alice", "alice-1")
	q.push("", "legacy-2")

	got := drain(t, q, 3)
	if got[0] != "legacy-1" || got[1] != "alice-1" {
		t.Errorf("picks = %v, want the ownerless bucket alternating like any other", got)
	}
}

// The old queue held 10000 ids and silently refused past that, after which the
// job waited for a restart. There is no ceiling now.
func TestNoBoundedBuffer(t *testing.T) {
	q := newFairQueue()
	const n = 20001
	for i := 0; i < n; i++ {
		q.push("bulk", strconv.Itoa(i))
	}
	if jobs, _ := q.pending(); jobs != n {
		t.Errorf("pending = %d, want %d — the queue dropped work", jobs, n)
	}
}

// take must return rather than block forever once the manager is shutting down,
// or Close would wait on a worker that never notices.
func TestTakeReturnsOnContextCancel(t *testing.T) {
	q := newFairQueue()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := q.take(ctx); ok {
			t.Error("take returned a job from an empty queue")
		}
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("take did not return after the context was cancelled")
	}
}

// A push arriving while a worker is already blocked must wake it.
func TestPushWakesAWaitingTake(t *testing.T) {
	q := newFairQueue()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	got := make(chan string, 1)
	go func() {
		id, ok := q.take(ctx)
		if ok {
			got <- id
		}
	}()

	// Give the goroutine time to reach the blocking select before pushing.
	time.Sleep(50 * time.Millisecond)
	q.push("late", "late-1")

	select {
	case id := <-got:
		if id != "late-1" {
			t.Errorf("woke with %q, want late-1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a push did not wake a blocked take")
	}
}
