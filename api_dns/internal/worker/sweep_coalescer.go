package worker

import (
	"context"
	"sync"
	"time"
)

// physicalSweepTimeout bounds one coalesced physical sweep. The sweep serves
// every caller that joined it, so it runs on its own deadline rather than on
// the context of whichever SyncDNS request started it.
const physicalSweepTimeout = 60 * time.Second

// sweepCoalescer runs at most one sweep per service type at a time. A caller
// arriving while a sweep runs joins the single sweep queued behind it, which
// starts after the running one ends. Joining the running sweep instead could
// return a result read before the caller's change, so it would not publish it;
// the queued sweep reads after every queued caller arrived.
type sweepCoalescer struct {
	mu     sync.Mutex
	byType map[string]*sweepQueue
}

type sweepQueue struct {
	running *sweepCall
	next    *sweepCall
}

type sweepCall struct {
	ctx    context.Context
	done   chan struct{}
	errors map[string]string
	err    error
}

// do runs sweep for serviceType, or waits for the sweep it joined, and returns
// that sweep's result. It returns ctx's error if ctx ends first; the sweep
// still completes for the other callers.
func (c *sweepCoalescer) do(ctx context.Context, serviceType string, sweep func(context.Context) (map[string]string, error)) (map[string]string, error) {
	c.mu.Lock()
	if c.byType == nil {
		c.byType = map[string]*sweepQueue{}
	}
	q := c.byType[serviceType]
	if q == nil {
		q = &sweepQueue{}
		c.byType[serviceType] = q
	}
	var call *sweepCall
	switch {
	case q.running == nil:
		call = &sweepCall{ctx: ctx, done: make(chan struct{})}
		q.running = call
		go c.run(serviceType, q, call, sweep)
	case q.next == nil:
		call = &sweepCall{ctx: ctx, done: make(chan struct{})}
		q.next = call
	default:
		call = q.next
	}
	c.mu.Unlock()

	select {
	case <-call.done:
		return call.errors, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *sweepCoalescer) run(serviceType string, q *sweepQueue, call *sweepCall, sweep func(context.Context) (map[string]string, error)) {
	for call != nil {
		sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(call.ctx), physicalSweepTimeout)
		call.errors, call.err = sweep(sweepCtx)
		cancel()
		close(call.done)

		c.mu.Lock()
		call = q.next
		q.running, q.next = call, nil
		if call == nil {
			delete(c.byType, serviceType)
		}
		c.mu.Unlock()
	}
}
