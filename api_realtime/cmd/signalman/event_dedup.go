package main

import (
	"sync"
	"time"
)

const (
	signalmanEventDedupCapacity = 65536
	signalmanEventDedupTTL      = 10 * time.Minute
)

// eventIDWindow remembers recently broadcast event IDs so an event delivered
// twice (MirrorMaker2 and producer retries are at-least-once) reaches live
// subscribers once. The window is bounded by capacity and TTL, so an event
// redelivered after eviction, or after a restart, is broadcast again: Signalman
// delivery is at-least-once, and this only suppresses common duplicates.
type eventIDWindow struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	slots []string
	next  int
	ids   map[string]eventIDEntry
}

type eventIDEntry struct {
	seenAt time.Time
	slot   int
}

func newEventIDWindow(capacity int, ttl time.Duration) *eventIDWindow {
	if capacity < 1 {
		capacity = 1
	}
	return &eventIDWindow{
		ttl:   ttl,
		now:   time.Now,
		slots: make([]string, capacity),
		ids:   make(map[string]eventIDEntry, capacity),
	}
}

// seen reports whether id was already recorded within the TTL, and records it
// when it was not. Empty IDs are never treated as duplicates.
func (w *eventIDWindow) seen(id string) bool {
	if id == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	if entry, ok := w.ids[id]; ok {
		if now.Sub(entry.seenAt) < w.ttl {
			return true
		}
		w.slots[entry.slot] = ""
		delete(w.ids, id)
	}

	if evicted := w.slots[w.next]; evicted != "" {
		delete(w.ids, evicted)
	}
	w.slots[w.next] = id
	w.ids[id] = eventIDEntry{seenAt: now, slot: w.next}
	w.next = (w.next + 1) % len(w.slots)
	return false
}
