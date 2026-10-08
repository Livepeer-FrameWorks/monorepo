package jobs

import (
	"testing"
	"time"
)

// TestRetryUnlessRanBacksOffWhileThePeerHoldsTheLock proves a replica that keeps losing the
// projection lock waits longer after each loss instead of retrying at a fixed short interval, and
// starts over once a pass runs.
func TestRetryUnlessRanBacksOffWhileThePeerHoldsTheLock(t *testing.T) {
	r := &ArtifactReconciler{lockRetryInterval: 50 * time.Millisecond}
	wait := func() time.Duration {
		start := time.Now()
		<-r.retryUnlessRan(false)
		return time.Since(start)
	}
	first, second, third := wait(), wait(), wait()
	if first > 90*time.Millisecond {
		t.Fatalf("first retry after %s, want about 50ms", first)
	}
	if third < 150*time.Millisecond {
		t.Fatalf("retries after %s, %s, %s; the third loss must wait about 200ms, not the first interval again", first, second, third)
	}
	if ch := r.retryUnlessRan(true); ch != nil {
		t.Fatal("a pass that ran still scheduled a retry")
	}
	if again := wait(); again > 90*time.Millisecond {
		t.Fatalf("first retry after a pass ran waited %s, want the backoff to start over at about 50ms", again)
	}
}
