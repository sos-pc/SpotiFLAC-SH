package main

import (
	"testing"
	"time"
)

func TestSSEHubPublishFanOutToAllSubscribers(t *testing.T) {
	h := newSSEHub()
	ch1 := h.subscribe()
	ch2 := h.subscribe()
	defer h.unsubscribe(ch1)
	defer h.unsubscribe(ch2)

	h.publish(JobEvent{Type: "job_update", Job: &Job{ID: "job-1"}})

	for i, ch := range []chan JobEvent{ch1, ch2} {
		select {
		case event := <-ch:
			if event.Job == nil || event.Job.ID != "job-1" {
				t.Errorf("subscriber %d got %+v, want job-1", i, event)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d did not receive the published event", i)
		}
	}
}

func TestSSEHubUnsubscribeStopsDelivery(t *testing.T) {
	h := newSSEHub()
	ch := h.subscribe()
	h.unsubscribe(ch)

	// unsubscribe closes the channel — receiving from it must return
	// immediately with ok=false, not block waiting for a value that will
	// never arrive.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after unsubscribe, got a value instead")
		}
	case <-time.After(time.Second):
		t.Fatal("receiving from an unsubscribed channel should not block")
	}

	// A publish after unsubscribe must not panic (send on closed channel)
	// or hang — the hub is expected to have removed ch from its subscriber
	// set before closing it.
	h.publish(JobEvent{Type: "job_update", Job: &Job{ID: "job-1"}})
}

// TestSSEHubPublishNonBlockingOnFullSubscriber is the regression test for
// the hub's core reliability guarantee: one slow/stuck client must never
// stall delivery to every other connected client. publish uses a
// non-blocking send (select/default) specifically so a full subscriber
// channel gets skipped instead of blocking the publisher.
func TestSSEHubPublishNonBlockingOnFullSubscriber(t *testing.T) {
	h := newSSEHub()
	slow := h.subscribe() // buffered 32, never drained in this test
	defer h.unsubscribe(slow)
	fast := h.subscribe()
	defer h.unsubscribe(fast)

	done := make(chan struct{})
	go func() {
		// One more than the channel's buffer (32) so at least one publish
		// hits the full-channel path for the slow subscriber.
		for i := 0; i < 40; i++ {
			h.publish(JobEvent{Type: "job_update", Job: &Job{ID: "job-1"}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publish blocked instead of skipping the full subscriber channel")
	}

	// The fast subscriber (drained concurrently below) should still have
	// received events despite the slow one being full — a broken hub that
	// blocks on one slow client would starve this one too.
	select {
	case <-fast:
	case <-time.After(time.Second):
		t.Error("a slow subscriber should not prevent delivery to other subscribers")
	}
}

func TestSSEHubSubscribeReturnsIndependentChannels(t *testing.T) {
	h := newSSEHub()
	ch1 := h.subscribe()
	defer h.unsubscribe(ch1)

	// Unsubscribing one channel must not affect another still-active one.
	ch2 := h.subscribe()
	defer h.unsubscribe(ch2)
	h.publish(JobEvent{Type: "job_update", Job: &Job{ID: "job-1"}})

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Error("ch1 should have received the event")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Error("ch2 should have received the event")
	}
}

// closeAll must make every subscriber's read loop terminate: the SSE handler
// blocks on `event, ok := <-ch` and only returns when ok is false, so closeAll
// closing the channels is what unblocks it and lets httpServer.Shutdown finish
// instead of waiting its 30s timeout.
func TestSSEHubCloseAllTerminatesEverySubscriber(t *testing.T) {
	h := newSSEHub()
	ch1 := h.subscribe()
	ch2 := h.subscribe()

	h.closeAll()

	for i, ch := range []chan JobEvent{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("subscriber %d received a value, want a closed channel", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d channel was not closed by closeAll", i)
		}
	}

	// The hub must forget them too, or a later publish would iterate closed chans.
	h.mu.RLock()
	n := len(h.subs)
	h.mu.RUnlock()
	if n != 0 {
		t.Errorf("closeAll left %d subscribers registered", n)
	}
}

// The SSE handler's defer calls unsubscribe(ch) precisely because closeAll just
// closed ch. That must NOT close it a second time — the real bug this guards is
// a "close of closed channel" panic during every shutdown.
func TestUnsubscribeAfterCloseAllDoesNotPanic(t *testing.T) {
	h := newSSEHub()
	ch := h.subscribe()

	h.closeAll()
	// Mirrors the handler's `defer h.unsubscribe(ch)` running after !ok.
	h.unsubscribe(ch) // must be a no-op, not a panic
}

// publish after closeAll must be safe: closeAll empties the map under the same
// lock publish takes, so there is no send on a closed channel and nothing to
// iterate.
func TestPublishAfterCloseAllIsSafe(t *testing.T) {
	h := newSSEHub()
	_ = h.subscribe()
	h.closeAll()
	h.publish(JobEvent{Type: "job_update", Job: &Job{ID: "after-close"}}) // must not panic
}

// Ordinary unsubscribe still closes the channel — the idempotence guard must
// not break the normal path.
func TestUnsubscribeStillClosesOnNormalPath(t *testing.T) {
	h := newSSEHub()
	ch := h.subscribe()
	h.unsubscribe(ch)
	if _, ok := <-ch; ok {
		t.Error("unsubscribe did not close the channel on the normal path")
	}
}
