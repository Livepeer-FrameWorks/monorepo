package cache

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

type Options struct {
	TTL                  time.Duration
	StaleWhileRevalidate time.Duration
	NegativeTTL          time.Duration
	MaxEntries           int
	// ValueLifetime can shorten or extend the positive-entry lifetime based on
	// the loaded value. It does not apply to negative entries or explicit Set.
	ValueLifetime func(key string, value interface{}) (ttl, staleWhileRevalidate time.Duration)
	// SkipStore, when non-nil and returning true for a key, suppresses both
	// positive and negative writes to that key. Used to enforce a hold-down
	// after invalidation so an in-flight loader cannot repopulate stale data.
	SkipStore func(key string) bool
}

type MetricsHooks struct {
	OnHit   func(labels map[string]string)
	OnMiss  func(labels map[string]string)
	OnStale func(labels map[string]string)
	OnStore func(labels map[string]string)
	OnError func(labels map[string]string)
}

type entry struct {
	value     interface{}
	err       error
	expiresAt time.Time
	staleAt   time.Time
	negative  bool
	lastUsed  time.Time
}

// loadFence must have non-zero size. Go permits pointers to distinct zero-sized
// allocations to compare equal, which would make an invalidated load's token
// indistinguishable from a newer load for the same key.
type loadFence [1]byte

type Cache struct {
	mu          sync.RWMutex
	items       map[string]*entry
	activeLoads map[string]*loadFence
	order       []string
	opts        Options
	metrics     MetricsHooks
	sf          singleflight.Group
}

// SnapshotEntry represents a point-in-time cache entry for debugging.
type SnapshotEntry struct {
	Key       string
	Value     interface{}
	Err       error
	ExpiresAt time.Time
	StaleAt   time.Time
	LastUsed  time.Time
	Negative  bool
}

func New(opts Options, hooks MetricsHooks) *Cache {
	return &Cache{
		items:       make(map[string]*entry),
		activeLoads: make(map[string]*loadFence),
		order:       make([]string, 0, 128),
		opts:        opts,
		metrics:     hooks,
	}
}

type Loader func(ctx context.Context, key string) (interface{}, bool, error)

type loadResult struct {
	val interface{}
	ok  bool
	err error
}

func (c *Cache) Get(ctx context.Context, key string, loader Loader) (interface{}, bool, error) {
	now := time.Now()
	c.mu.RLock()
	if e, ok := c.items[key]; ok {
		if now.Before(e.expiresAt) {
			c.mu.RUnlock()
			if c.metrics.OnHit != nil {
				c.metrics.OnHit(map[string]string{"key": key})
			}
			if e.negative {
				return nil, false, e.err
			}
			return e.value, true, nil
		}
		if now.Before(e.staleAt) {
			// SWR: return stale and refresh in background once
			if c.metrics.OnStale != nil {
				c.metrics.OnStale(map[string]string{"key": key})
			}
			go func() {
				// The loader owns its operation deadline. The request that happened
				// to notice staleness may stop waiting, but must not cancel refresh
				// work shared by later callers.
				refreshCtx := context.WithoutCancel(ctx)
				// x/sync/singleflight.Group.Do returns (val, err, shared) in this version.
				_, err, _ := c.sf.Do("refresh:"+key, func() (interface{}, error) {
					fence := c.beginLoad(key)
					defer c.endLoad(key, fence)
					c.refresh(refreshCtx, key, fence, loader)
					return nil, nil
				})
				if err != nil {
					if c.metrics.OnError != nil {
						c.metrics.OnError(map[string]string{"key": key})
					}
				}
			}()
			val, ok := e.value, !e.negative
			c.mu.RUnlock()
			if ok {
				return val, true, nil
			}
			return nil, false, e.err
		}
		// Hard expired: drop and load synchronously
		expired := e
		c.mu.RUnlock()
		c.mu.Lock()
		// Recheck the exact entry after upgrading the lock: another goroutine
		// may have completed a refresh between the read and write locks.
		if current, exists := c.items[key]; exists && current == expired && !now.Before(current.staleAt) {
			delete(c.items, key)
			c.removeFromOrder(key)
		}
		c.mu.Unlock()
	} else {
		c.mu.RUnlock()
	}

	if c.metrics.OnMiss != nil {
		c.metrics.OnMiss(map[string]string{"key": key})
	}
	resultCh := c.sf.DoChan(key, func() (interface{}, error) {
		// A prior singleflight may have completed after this caller observed a
		// miss but before it joined the group. Recheck under the group closure so
		// late arrivals do not start a second authority load.
		if current, found := c.freshResult(key); found {
			return current, nil
		}
		fence := c.beginLoad(key)
		defer c.endLoad(key, fence)
		// Each waiter observes its own context below. Shared work is detached
		// from the leader so a short-deadline caller cannot poison followers;
		// the loader must apply the deadline appropriate to its operation.
		loadCtx := context.WithoutCancel(ctx)
		val, ok, err := loader(loadCtx, key)
		c.storeFenced(key, fence, val, ok, err)
		return loadResult{val: val, ok: ok, err: err}, nil
	})
	var result singleflight.Result
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case result = <-resultCh:
	}
	if result.Err != nil {
		// singleflight function should always return nil error, but be defensive
		if c.metrics.OnError != nil {
			c.metrics.OnError(map[string]string{"key": key})
		}
		return nil, false, result.Err
	}
	res, ok := result.Val.(loadResult)
	if !ok {
		if c.metrics.OnError != nil {
			c.metrics.OnError(map[string]string{"key": key})
		}
		return nil, false, context.Canceled // shouldn't happen; indicates programmer error
	}
	if !res.ok {
		return nil, false, res.err
	}
	return res.val, true, nil
}

func (c *Cache) freshResult(key string) (loadResult, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || !now.Before(e.expiresAt) {
		return loadResult{}, false
	}
	if e.negative {
		return loadResult{err: e.err}, true
	}
	return loadResult{val: e.value, ok: true}, true
}

func (c *Cache) refresh(ctx context.Context, key string, fence *loadFence, loader Loader) {
	val, ok, err := loader(ctx, key)
	c.storeFenced(key, fence, val, ok, err)
}

// store is kept as the direct write path for package-level cache tests. Normal
// loads use storeFenced so Delete can prevent stale in-flight publication.
func (c *Cache) store(key string, val interface{}, ok bool, err error) {
	fence := c.beginLoad(key)
	defer c.endLoad(key, fence)
	c.storeFenced(key, fence, val, ok, err)
}

func (c *Cache) beginLoad(key string) *loadFence {
	fence := &loadFence{}
	c.mu.Lock()
	c.activeLoads[key] = fence
	c.mu.Unlock()
	return fence
}

func (c *Cache) endLoad(key string, fence *loadFence) {
	c.mu.Lock()
	if c.activeLoads[key] == fence {
		delete(c.activeLoads, key)
	}
	c.mu.Unlock()
}

func (c *Cache) storeFenced(key string, fence *loadFence, val interface{}, ok bool, err error) {
	if c.opts.SkipStore != nil && c.opts.SkipStore(key) {
		return
	}
	now := time.Now()
	e := &entry{lastUsed: now}
	if ok {
		ttl, swr := c.valueLifetime(key, val)
		e.value = val
		e.expiresAt = now.Add(ttl)
		e.staleAt = e.expiresAt.Add(swr)
		e.negative = false
	} else {
		if c.opts.NegativeTTL <= 0 {
			// Do not store negatives
			if c.metrics.OnError != nil {
				c.metrics.OnError(map[string]string{"key": key})
			}
			return
		}
		e.err = err
		e.negative = true
		e.expiresAt = now.Add(c.opts.NegativeTTL)
		e.staleAt = e.expiresAt
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeLoads[key] != fence {
		return
	}
	if prev, exists := c.items[key]; exists {
		// preserve order position
		_ = prev
	} else {
		c.order = append(c.order, key)
	}
	c.items[key] = e
	c.evictIfNeeded()
	if c.metrics.OnStore != nil {
		c.metrics.OnStore(map[string]string{"key": key, "ok": boolStr(ok)})
	}
}

func (c *Cache) valueLifetime(key string, val interface{}) (time.Duration, time.Duration) {
	if c.opts.ValueLifetime != nil {
		return c.opts.ValueLifetime(key, val)
	}
	return c.opts.TTL, c.opts.StaleWhileRevalidate
}

func (c *Cache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *Cache) evictIfNeeded() {
	if c.opts.MaxEntries <= 0 || len(c.items) <= c.opts.MaxEntries {
		return
	}
	// Simple FIFO eviction; can be replaced with true LRU
	excess := len(c.items) - c.opts.MaxEntries
	for excess > 0 && len(c.order) > 0 {
		victim := c.order[0]
		c.order = c.order[1:]
		delete(c.items, victim)
		excess--
	}
}

func (c *Cache) Set(key string, val interface{}, ttl time.Duration) {
	if c.opts.SkipStore != nil && c.opts.SkipStore(key) {
		return
	}
	now := time.Now()
	e := &entry{value: val, expiresAt: now.Add(ttl), staleAt: now.Add(ttl).Add(c.opts.StaleWhileRevalidate), lastUsed: now}
	c.mu.Lock()
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = e
	c.evictIfNeeded()
	c.mu.Unlock()
}

func (c *Cache) SetDefault(key string, val interface{}) {
	if c.opts.SkipStore != nil && c.opts.SkipStore(key) {
		return
	}
	ttl, swr := c.valueLifetime(key, val)
	now := time.Now()
	e := &entry{value: val, expiresAt: now.Add(ttl), staleAt: now.Add(ttl).Add(swr), lastUsed: now}
	c.mu.Lock()
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	c.items[key] = e
	c.evictIfNeeded()
	c.mu.Unlock()
}

// Peek returns a cached value without triggering a load. Stale entries are allowed.
func (c *Cache) Peek(key string) (interface{}, bool) {
	value, ok, _ := c.PeekWithFreshness(key)
	return value, ok
}

// PeekWithFreshness returns a cached value without loading and reports whether it is in the SWR window.
func (c *Cache) PeekWithFreshness(key string) (interface{}, bool, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false, false
	}
	if now.After(e.staleAt) {
		return nil, false, false
	}
	if e.negative {
		return nil, false, false
	}
	return e.value, true, !now.Before(e.expiresAt)
}

// PeekHardExpired returns a positive value only after its stale window has
// ended. Callers may use it for bounded diagnostics, but must never authorize
// from the returned value.
func (c *Cache) PeekHardExpired(key string) (interface{}, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok || e.negative || now.Before(e.staleAt) {
		return nil, false
	}
	return e.value, true
}

// Snapshot returns a copy of current cache entries for debugging/inspection.
func (c *Cache) Snapshot() []SnapshotEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]SnapshotEntry, 0, len(c.items))
	for k, e := range c.items {
		out = append(out, SnapshotEntry{
			Key:       k,
			Value:     e.value,
			Err:       e.err,
			ExpiresAt: e.expiresAt,
			StaleAt:   e.staleAt,
			LastUsed:  e.lastUsed,
			Negative:  e.negative,
		})
	}
	return out
}

func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.items, key)
	c.removeFromOrder(key)
	delete(c.activeLoads, key)
	c.mu.Unlock()
	c.sf.Forget(key)
	c.sf.Forget("refresh:" + key)
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
