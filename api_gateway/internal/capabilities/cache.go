// Package capabilities caches the sections of the capabilities query. The
// cache only describes enforcement for clients; no enforcement path reads it.
package capabilities

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SectionTTL is how long a section read from its source is served without
// asking the source again.
const SectionTTL = 30 * time.Second

// Section is one cached capabilities section and when its source produced it.
type Section[T any] struct {
	Value      T
	ObservedAt time.Time
	// Stale is true when the source failed and an older reading is served.
	Stale bool
}

type entry[T any] struct {
	value      T
	observedAt time.Time
}

// SectionCache holds one section per tenant.
type SectionCache[T any] struct {
	ttl     time.Duration
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]entry[T]
}

// NewSectionCache returns a cache with SectionTTL and the wall clock.
func NewSectionCache[T any]() *SectionCache[T] {
	return newSectionCache[T](SectionTTL, time.Now)
}

// NewSectionCacheWithClock returns a cache with SectionTTL that reads time from
// now, so a caller can drive expiry instead of waiting for it.
func NewSectionCacheWithClock[T any](now func() time.Time) *SectionCache[T] {
	return newSectionCache[T](SectionTTL, now)
}

func newSectionCache[T any](ttl time.Duration, now func() time.Time) *SectionCache[T] {
	return &SectionCache[T]{ttl: ttl, now: now, entries: map[string]entry[T]{}}
}

// Get returns the tenant's section. A reading younger than the TTL is served
// as is. Otherwise load is called; on success its value replaces the entry. A
// PermissionDenied or Unauthenticated failure removes the entry and returns
// the error. On any other failure the last reading is served with its
// original ObservedAt and Stale set, and without one the load error is
// returned so the section resolves to null with an error instead of a
// made-up value.
func (c *SectionCache[T]) Get(ctx context.Context, tenantID string, load func(context.Context) (T, error)) (Section[T], error) {
	now := c.now()
	c.mu.Lock()
	cached, ok := c.entries[tenantID]
	c.mu.Unlock()
	if ok && now.Sub(cached.observedAt) < c.ttl {
		return Section[T]{Value: cached.value, ObservedAt: cached.observedAt}, nil
	}

	value, err := load(ctx)
	if err != nil {
		// A refusal is the source's answer about this caller's access, so
		// the reading is dropped rather than served as stale.
		if code := status.Code(err); code == codes.PermissionDenied || code == codes.Unauthenticated {
			c.mu.Lock()
			delete(c.entries, tenantID)
			c.mu.Unlock()
			var zero T
			return Section[T]{Value: zero}, err
		}
		if ok {
			return Section[T]{Value: cached.value, ObservedAt: cached.observedAt, Stale: true}, nil
		}
		var zero T
		return Section[T]{Value: zero}, err
	}
	observedAt := c.now()
	c.mu.Lock()
	if current, exists := c.entries[tenantID]; !exists || !current.observedAt.After(observedAt) {
		c.entries[tenantID] = entry[T]{value: value, observedAt: observedAt}
	}
	c.mu.Unlock()
	return Section[T]{Value: value, ObservedAt: observedAt}, nil
}
