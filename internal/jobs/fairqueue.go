package jobs

import (
	"context"
	"sync"
)

// fairQueue hands pending jobs to workers one at a time, cycling between the
// users who have work waiting.
//
// It replaces a buffered channel, which was strictly first-in-first-out. That
// is the right shape for one user and the wrong one for a household: a
// watchlist sync enqueues one job per track, and the reference deployment has a
// playlist of 2561 — so whoever synced it held the only worker for hours while
// everyone else's single track waited behind all 2561.
//
// Round-robin makes nothing faster. Four people with one track each behind a
// 2561-track sync now finish in four downloads instead of 2564, and the sync
// finishes exactly when it would have. Only the order changes, which is the
// entire complaint.
//
// Fairness is between PEOPLE, not between kinds of work: an interactive
// download is not prioritised over a scheduled sync. That is a second axis, it
// is tempting, and it should wait until this one has been lived with.
//
// Raising the worker count is NOT the alternative, and not merely for caution.
// The engine's own /download handler wraps the work in redirect_stdout, which
// swaps a process-global. Two concurrent calls interleave their
// save-and-restore, and the process is left writing its logs into an abandoned
// buffer — so concurrency there loses all engine logging rather than being
// merely unsafe.
//
// This also retires the bounded buffer. The channel held 10000 ids; a full one
// made Submit report queued=false, after which the job waited for
// recoverPendingJobs — which runs at STARTUP and nowhere else, despite a log
// line promising the "next poll". Four syncs the size of the reference playlist
// exceeded it, and the symptom was downloads that never started, with nothing
// saying why, cured only by a restart nobody would think to perform.
type fairQueue struct {
	mu sync.Mutex
	// Pending job ids per owner, FIFO within an owner. A user's own downloads
	// keep the order they were asked for; only the interleaving between users
	// is new.
	byUser map[string][]string
	// Owners with work, in the order they first appeared. The cursor walks it.
	order  []string
	cursor int
	closed bool

	// A single buffered slot is enough: it means "there may be work now", and
	// take() re-checks under the lock. Shutdown does not signal here — the
	// manager cancels its context first, and take() selects on that.
	wake chan struct{}
}

func newFairQueue() *fairQueue {
	return &fairQueue{
		byUser: make(map[string][]string),
		wake:   make(chan struct{}, 1),
	}
}

// push adds a job for owner. It never blocks and never refuses — an unbounded
// queue is the point, see the type comment.
//
// owner may be empty: jobs predating authentication have no UserID, and they
// form their own bucket rather than being silently attributed to someone.
func (q *fairQueue) push(owner, jobID string) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if _, known := q.byUser[owner]; !known {
		q.order = append(q.order, owner)
	}
	q.byUser[owner] = append(q.byUser[owner], jobID)
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default: // a wake-up is already pending; one is as good as many
	}
}

// take blocks until a job is available, returning false once ctx is done or the
// queue is closed.
func (q *fairQueue) take(ctx context.Context) (string, bool) {
	for {
		q.mu.Lock()
		id, ok := q.nextLocked()
		closed := q.closed
		q.mu.Unlock()
		if ok {
			return id, true
		}
		if closed {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-q.wake:
		}
	}
}

// nextLocked takes one job and leaves the cursor on the FOLLOWING owner, so the
// next call serves someone else. Callers hold q.mu.
func (q *fairQueue) nextLocked() (string, bool) {
	// At most one lap: every iteration either returns or removes an owner.
	for attempts := len(q.order); attempts > 0; attempts-- {
		if len(q.order) == 0 {
			return "", false
		}
		if q.cursor >= len(q.order) {
			q.cursor = 0
		}
		owner := q.order[q.cursor]
		pending := q.byUser[owner]
		if len(pending) == 0 {
			// Drop an owner with nothing left. The cursor is not advanced:
			// removing at this index already makes it point at the next one.
			q.order = append(q.order[:q.cursor], q.order[q.cursor+1:]...)
			delete(q.byUser, owner)
			continue
		}
		id := pending[0]
		if len(pending) == 1 {
			// Delete rather than keep an empty slice, so the backing array of a
			// 2561-entry batch is released as soon as it drains instead of
			// living until the next lap notices.
			delete(q.byUser, owner)
			q.order = append(q.order[:q.cursor], q.order[q.cursor+1:]...)
		} else {
			q.byUser[owner] = pending[1:]
			q.cursor++
		}
		return id, true
	}
	return "", false
}

// close stops accepting work. It deliberately does not wake anyone: the manager
// cancels its context first, and take() is waiting on that.
func (q *fairQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
}

// pending reports how many jobs are waiting, and for how many distinct owners.
func (q *fairQueue) pending() (jobs, owners int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ids := range q.byUser {
		jobs += len(ids)
	}
	return jobs, len(q.byUser)
}
