package federation

import (
	"container/list"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"frameworks/api_balancing/internal/balancer"
)

const (
	placementObservationReuse      = time.Second
	placementObservationLoadMax    = 5 * time.Second
	placementObservationEntries    = 128
	placementObservationCandidates = 16384
)

type placementObservationKey struct {
	CellID, Address, Query       string
	TenantVersion, ObjectVersion int64
}

type placementObservationEntry struct {
	key   placementObservationKey
	value balancer.PlacementCellObservation
	until time.Time
}

type placementObservationFlight struct {
	done  chan struct{}
	value balancer.PlacementCellObservation
	err   error
}

// PlacementObservationCache reuses complete remote facts, never a placement
// decision. Local inventory, policy evaluation and preparation stay uncached.
type PlacementObservationCache struct {
	mu         sync.Mutex
	entries    map[placementObservationKey]*list.Element
	flights    map[placementObservationKey]*placementObservationFlight
	lru        list.List
	candidates int
	now        func() time.Time
}

func NewPlacementObservationCache(now func() time.Time) *PlacementObservationCache {
	if now == nil {
		now = time.Now
	}
	return &PlacementObservationCache{entries: make(map[placementObservationKey]*list.Element), flights: make(map[placementObservationKey]*placementObservationFlight), now: now}
}

func (cache *PlacementObservationCache) observe(ctx context.Context, key placementObservationKey, load func(context.Context) (balancer.PlacementCellObservation, error)) (balancer.PlacementCellObservation, error) {
	if err := ctx.Err(); err != nil {
		return balancer.PlacementCellObservation{}, err
	}
	cache.mu.Lock()
	if entry := cache.entries[key]; entry != nil {
		stored, ok := entry.Value.(placementObservationEntry)
		if !ok {
			panic("invalid placement observation cache entry")
		}
		if cache.now().Before(stored.until) && stored.value.Fresh(cache.now()) {
			cache.lru.MoveToFront(entry)
			result := clonePlacementObservation(stored.value)
			cache.mu.Unlock()
			return result, ctx.Err()
		}
		cache.remove(entry)
	}
	flight := cache.flights[key]
	if flight == nil {
		if len(cache.flights) >= placementObservationEntries {
			cache.mu.Unlock()
			return balancer.PlacementCellObservation{}, errors.New("placement observation concurrency limit reached")
		}
		flight = &placementObservationFlight{done: make(chan struct{})}
		cache.flights[key] = flight
		// A canceled viewer must not cancel the shared read for other viewers.
		// Preserve the routing deadline when detaching cancellation so the cache
		// cannot silently truncate the caller's observation budget.
		sharedBase := context.WithoutCancel(ctx)
		var sharedCtx context.Context
		var cancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			sharedCtx, cancel = context.WithDeadline(sharedBase, deadline)
		} else {
			sharedCtx, cancel = context.WithTimeout(sharedBase, placementObservationLoadMax)
		}
		go func() {
			defer cancel()
			value, err := load(sharedCtx)
			if sharedCtx.Err() != nil {
				value, err = balancer.PlacementCellObservation{}, sharedCtx.Err()
			}
			cache.finish(key, flight, value, err)
		}()
	}
	cache.mu.Unlock()
	select {
	case <-ctx.Done():
		return balancer.PlacementCellObservation{}, ctx.Err()
	case <-flight.done:
		if err := ctx.Err(); err != nil {
			return balancer.PlacementCellObservation{}, err
		}
		return clonePlacementObservation(flight.value), flight.err
	}
}

func (cache *PlacementObservationCache) finish(key placementObservationKey, flight *placementObservationFlight, value balancer.PlacementCellObservation, err error) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	flight.value, flight.err = clonePlacementObservation(value), err
	delete(cache.flights, key)
	now := cache.now()
	if err == nil && value.Complete && value.Fresh(now) && len(value.Candidates) <= 4096 {
		until := now.Add(placementObservationReuse)
		if value.ExpiresAt.Before(until) {
			until = value.ExpiresAt
		}
		if now.Before(until) {
			for cache.lru.Len() >= placementObservationEntries || cache.candidates+len(value.Candidates) > placementObservationCandidates {
				cache.remove(cache.lru.Back())
			}
			cache.entries[key] = cache.lru.PushFront(placementObservationEntry{key: key, value: flight.value, until: until})
			cache.candidates += len(value.Candidates)
		}
	}
	close(flight.done)
}

func (cache *PlacementObservationCache) remove(entry *list.Element) {
	stored, ok := entry.Value.(placementObservationEntry)
	if !ok {
		panic("invalid placement observation cache entry")
	}
	cache.candidates -= len(stored.value.Candidates)
	delete(cache.entries, stored.key)
	cache.lru.Remove(entry)
}

func clonePlacementObservation(value balancer.PlacementCellObservation) balancer.PlacementCellObservation {
	value.Candidates = slices.Clone(value.Candidates)
	for i := range value.Candidates {
		candidate := &value.Candidates[i]
		candidate.AllowedVerbs = slices.Clone(candidate.AllowedVerbs)
		candidate.Prices = slices.Clone(candidate.Prices)
		if candidate.Location != nil {
			location := *candidate.Location
			candidate.Location = &location
		}
		if candidate.Price != nil {
			price := *candidate.Price
			candidate.Price = &price
		}
	}
	return value
}
