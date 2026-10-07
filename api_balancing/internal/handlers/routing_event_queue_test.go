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

	event := func() *RoutingEvent { return &RoutingEvent{Status: "success", StreamID: routingTestStreamID} }
	if !enqueueRoutingEvent(nil, event()) {
		t.Fatal("first event should be accepted")
	}
	if !enqueueRoutingEvent(nil, event()) {
		t.Fatal("second event should be accepted")
	}
	if enqueueRoutingEvent(nil, event()) {
		t.Fatal("third event should be dropped, not queued")
	}
	if enqueueRoutingEvent(nil, event()) {
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

	if enqueueRoutingEvent(nil, &RoutingEvent{Status: "success", StreamID: routingTestStreamID}) {
		t.Fatal("enqueue must report not-accepted when the queue is not started")
	}
}

const routingTestStreamID = "5d0f6c1e-2b8a-4c39-9a51-7e3f0b6d2c41"

// Periscope keeps a routing decision only when it names its content, so a
// lookup that resolved to no stream or artifact (a probe of an unknown stream
// key) is never sent; it would otherwise be counted as a dropped event.
func TestRoutingEventWithoutContentIdentityIsNotSent(t *testing.T) {
	prevQueue := routingEventQueue
	routingEventQueue = make(chan queuedRoutingEvent, 4)
	t.Cleanup(func() { routingEventQueue = prevQueue })

	if enqueueRoutingEvent(nil, &RoutingEvent{Status: "failed", StreamName: "live+unknown-key", StreamTenantID: "tenant"}) {
		t.Fatal("a routing event with no stream_id or artifact_hash was queued for Decklog")
	}
	if len(routingEventQueue) != 0 {
		t.Fatalf("queue depth: got %d want 0", len(routingEventQueue))
	}
	if !enqueueRoutingEvent(nil, &RoutingEvent{Status: "success", StreamID: routingTestStreamID}) {
		t.Fatal("a routing event naming its stream was not queued")
	}
	if !enqueueRoutingEvent(nil, &RoutingEvent{Status: "success", ArtifactHash: "0123456789abcdef0123456789abcdef"}) {
		t.Fatal("a routing event naming its artifact was not queued")
	}
}
