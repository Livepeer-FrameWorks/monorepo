package social

import "sync"

// EventCollector accumulates signals from external sources (crawler, heartbeat)
// for the social agent to consume on its next cycle.
type EventCollector struct {
	mu     sync.Mutex
	events []EventSignal
	notify chan struct{}
}

func NewEventCollector() *EventCollector {
	return &EventCollector{notify: make(chan struct{}, 1)}
}

// Push adds a signal to the collector. Thread-safe.
func (c *EventCollector) Push(signal EventSignal) {
	c.mu.Lock()
	// Cap at 100 to avoid unbounded growth between drain cycles
	if len(c.events) < 100 {
		c.events = append(c.events, signal)
	}
	c.mu.Unlock()

	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// Drain returns all accumulated signals and resets the buffer. Thread-safe.
func (c *EventCollector) Drain() []EventSignal {
	c.mu.Lock()
	defer c.mu.Unlock()
	events := c.events
	c.events = nil
	return events
}

// Notify fires when at least one signal is available. The channel is buffered
// and coalesces bursts; consumers should call Drain to inspect the events.
func (c *EventCollector) Notify() <-chan struct{} {
	return c.notify
}
