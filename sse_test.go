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
