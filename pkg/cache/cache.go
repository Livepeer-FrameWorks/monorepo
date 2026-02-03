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

type Cache struct {
	mu      sync.RWMutex
	items   map[string]*entry
	order   []string
	opts    Options
	metrics MetricsHooks
	sf      singleflight.Group
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
		items:   make(map[string]*entry),
		order:   make([]string, 0, 128),
		opts:    opts,
		metrics: hooks,
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
			e.lastUsed = now
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
				_, _, _ = c.sf.Do("refresh:"+key, func() (interface{}, error) {
					c.refresh(ctx, key, loader)
					return nil, nil
				})
			}()
			val, ok := e.value, !e.negative
			c.mu.RUnlock()
			if ok {
				return val, true, nil
			}
			return nil, false, e.err
		}
		// Hard expired: drop and load synchronously
		c.mu.RUnlock()
		c.mu.Lock()
		delete(c.items, key)
		c.removeFromOrder(key)
		c.mu.Unlock()
	} else {
		c.mu.RUnlock()
	}

	if c.metrics.OnMiss != nil {
		c.metrics.OnMiss(map[string]string{"key": key})
	}
	result, _, _ := c.sf.Do(key, func() (interface{}, error) {
		val, ok, err := loader(ctx, key)
		c.store(key, val, ok, err)
		return loadResult{val: val, ok: ok, err: err}, nil
	})
	res := result.(loadResult)
	if !res.ok {
		return nil, false, res.err
	}
	return res.val, true, nil
}

func (c *Cache) refresh(ctx context.Context, key string, loader Loader) {
	val, ok, err := loader(ctx, key)
	c.store(key, val, ok, err)
}

func (c *Cache) store(key string, val interface{}, ok bool, err error) {
	now := time.Now()
	e := &entry{lastUsed: now}
	if ok {
		e.value = val
		e.expiresAt = now.Add(c.opts.TTL)
		e.staleAt = e.expiresAt.Add(c.opts.StaleWhileRevalidate)
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

// Peek returns a cached value without triggering a load. Stale entries are allowed.
func (c *Cache) Peek(key string) (interface{}, bool) {
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if now.After(e.staleAt) {
		return nil, false
	}
	if e.negative {
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
	c.mu.Unlock()
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
