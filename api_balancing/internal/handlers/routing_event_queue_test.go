package handlers

import (
	"testing"
)

// The Decklog send is blocking, so telemetry must never be emitted as a
// goroutine per event: during an outage that grows without bound behind
// requests that themselves succeeded. A full queue sheds the event instead,
// and the caller is never blocked.
func TestRoutingEventQueueDropsWhenFull(t *testing.T) {
	prevQueue := routingEventQueue
	// A queue with no workers draining it stands in for a stalled Decklog.
	routingEventQueue = make(chan queuedRoutingEvent, 2)
	t.Cleanup(func() { routingEventQueue = prevQueue })

	before := RoutingEventsDropped()

	if !enqueueRoutingEvent(nil, &RoutingEvent{Status: "success"}) {
		t.Fatal("first event should be accepted")
	}
	if !enqueueRoutingEvent(nil, &RoutingEvent{Status: "success"}) {
		t.Fatal("second event should be accepted")
	}
	if enqueueRoutingEvent(nil, &RoutingEvent{Status: "success"}) {
		t.Fatal("third event should be dropped, not queued")
	}
	if enqueueRoutingEvent(nil, &RoutingEvent{Status: "success"}) {
		t.Fatal("further events should keep being dropped")
	}

	if dropped := RoutingEventsDropped() - before; dropped != 2 {
		t.Fatalf("dropped counter: got %d want 2", dropped)
	}
	if len(routingEventQueue) != 2 {
		t.Fatalf("queue depth: got %d want 2", len(routingEventQueue))
	}
}

// Before Init runs (and in unit tests), there is no queue; enqueueing must be a
// no-op rather than falling back to an unbounded goroutine.
func TestRoutingEventQueueNoOpBeforeStart(t *testing.T) {
	prevQueue := routingEventQueue
	routingEventQueue = nil
	t.Cleanup(func() { routingEventQueue = prevQueue })

	if enqueueRoutingEvent(nil, &RoutingEvent{Status: "success"}) {
		t.Fatal("enqueue must report not-accepted when the queue is not started")
	}
}
