package social

import "sync"

// EventCollector accumulates signals from external sources (crawler, heartbeat)
// for the social agent to consume on its next cycle.
type EventCollector struct {
	mu     sync.Mutex
	events []EventSignal
}

func NewEventCollector() *EventCollector {
	return &EventCollector{}
}

// Push adds a signal to the collector. Thread-safe.
func (c *EventCollector) Push(signal EventSignal) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Cap at 100 to avoid unbounded growth between drain cycles
	if len(c.events) < 100 {
		c.events = append(c.events, signal)
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
