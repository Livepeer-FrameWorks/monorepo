package control

import (
	"testing"
	"time"
)

// A Foghorn that announced it is going away has closed its listener, so the
// address resolves to the rest of the cell. The redial therefore happens at once
// and then about once a second, and the ordinary backoff only paces the redial
// again once the window after the notice has run out.
func TestRedialAfterGoingAwayIsImmediateThenFlat(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for range 50 {
		if delay := nextReconnectDelay(30*time.Second, now, now, true); delay > goingAwayRedialInterval/4 {
			t.Fatalf("first redial after the notice waited %s", delay)
		}
		if delay := nextReconnectDelay(30*time.Second, now, now.Add(10*time.Second), false); delay < goingAwayRedialInterval/2 || delay > 2*goingAwayRedialInterval {
			t.Fatalf("redial inside the window waited %s, want about %s whatever the backoff says", delay, goingAwayRedialInterval)
		}
		if delay := nextReconnectDelay(8*time.Second, now, now.Add(goingAwayRedialWindow+time.Second), false); delay < 4*time.Second {
			t.Fatalf("redial after the window waited %s, want the backoff again", delay)
		}
		if delay := nextReconnectDelay(8*time.Second, time.Time{}, now, false); delay < 4*time.Second {
			t.Fatalf("a disconnect nobody announced waited %s, want the backoff", delay)
		}
	}
}

// Requests already sent hold a Mist trigger each. The connection ends only once
// they are answered, and never later than the drain allows.
func TestGoingAwayWaitsForRequestsInFlight(t *testing.T) {
	resetControlState(t)
	pendingMutex <- struct{}{}
	pendingMistTriggers["request-1"] = pendingMistTrigger{}
	<-pendingMutex

	answered := time.AfterFunc(60*time.Millisecond, func() {
		pendingMutex <- struct{}{}
		delete(pendingMistTriggers, "request-1")
		<-pendingMutex
	})
	defer answered.Stop()
	started := time.Now()
	awaitInFlightMistTriggers(2 * time.Second)
	if waited := time.Since(started); waited < 50*time.Millisecond || waited > time.Second {
		t.Fatalf("waited %s for a request answered after 60ms", waited)
	}

	pendingMutex <- struct{}{}
	pendingMistTriggers["request-2"] = pendingMistTrigger{}
	<-pendingMutex
	started = time.Now()
	awaitInFlightMistTriggers(80 * time.Millisecond)
	if waited := time.Since(started); waited < 80*time.Millisecond || waited > time.Second {
		t.Fatalf("waited %s for a request that is never answered, want the drain limit", waited)
	}
}
