package jobs

import (
	"context"
	"time"
)

// stoppable returns a context that ends after timeout or as soon as stop is
// closed, whichever comes first. Background jobs run their passes under it, so
// a job's Stop cancels in-flight database, object-store and node calls instead
// of waiting them out: Foghorn stops these jobs after its listeners have
// closed, and an edge marks itself disconnected when a restarted Foghorn does
// not return within its announced-restart deadline. Every job tolerates a pass
// that ends mid-way, as it tolerates a crash, and picks the work up on a later
// pass. A nil stop only applies the timeout.
func stoppable(stop <-chan struct{}, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if stop != nil {
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

// untilStopped returns a context that ends once stop is closed.
func untilStopped(stop <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	if stop != nil {
		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}
