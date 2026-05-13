// Package policybundle holds Foghorn's in-memory cache of Commodore-minted
// signed policy bundles. The cache survives Commodore outages up to the
// bundle's hard TTL; revocations from Commodore propagate via the existing
// playback_policy_invalidation_outbox channel and bump a per-(tenant,
// stream) watermark on the cache, invalidating cached entries below the
// minimum acceptable bundle_version.
package policybundle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is a snapshot of one cached bundle.
type Entry struct {
	BundleJWT     string
	BundleVersion int64
	IssuedAt      time.Time
	SoftExpiresAt time.Time
	ExpiresAt     time.Time
}

// FetchFunc is the callback the cache invokes when it needs a fresh bundle.
// It returns the new entry or an error; transient errors propagate to the
// caller of Get when there is no usable cached entry.
type FetchFunc func(ctx context.Context, tenantID, streamID string) (Entry, error)

// Cache is a goroutine-safe (tenant_id, stream_id) → Entry store with
// soft/hard TTL semantics and a per-key minimum-acceptable bundle_version
// watermark.
//
// Soft TTL semantics: a cached entry past its SoftExpiresAt is still returned
// to the caller; the cache kicks an async refresh so the next read sees a
// fresh bundle. This avoids serializing concurrent reads behind a cold-cache
// fetch under load.
//
// Hard TTL semantics: a cached entry past its ExpiresAt is treated as
// missing; Get must fetch synchronously and return the new entry.
//
// Watermark semantics: BumpWatermark(tenantID, streamID, minVersion) sets
// the minimum-acceptable bundle_version for that key. Subsequent Get calls
// treat any cached entry with BundleVersion < minVersion as missing and
// force a refresh.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]*cacheRow

	// inflight tracks per-key refresh goroutines so concurrent soft-refresh
	// triggers do not stampede the fetch function.
	inflight sync.Map // key -> *atomic.Bool
}

type cacheRow struct {
	entry     Entry
	watermark int64 // minimum acceptable BundleVersion
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{entries: map[string]*cacheRow{}}
}

func cacheKey(tenantID, streamID string) string {
	return tenantID + "\x1f" + streamID
}

// ErrFetchFailed is returned by Get when the cache has no usable entry and
// the FetchFunc returns an error. Callers should propagate to admission as
// a fail-closed deny.
var ErrFetchFailed = errors.New("policybundle: fetch failed and no usable cached entry")

// Get returns the JWT bundle for (tenantID, streamID).
//
// Hot path: cache HIT under soft TTL → return cached, no fetch.
// Warm path: cache HIT past soft TTL but under hard TTL → return cached,
//
//	kick a background refresh that updates the entry when it completes.
//
// Cold path: cache MISS or past hard TTL or under watermark → call fetcher
//
//	synchronously; on success update the cache and return; on error
//	return ErrFetchFailed wrapping the cause.
func (c *Cache) Get(ctx context.Context, tenantID, streamID string, fetch FetchFunc, now time.Time) (Entry, error) {
	key := cacheKey(tenantID, streamID)

	c.mu.RLock()
	row, ok := c.entries[key]
	c.mu.RUnlock()

	if ok && row.entry.BundleVersion >= row.watermark && now.Before(row.entry.ExpiresAt) {
		if now.Before(row.entry.SoftExpiresAt) {
			return row.entry, nil
		}
		// Soft-expired: serve cached, background-refresh.
		c.triggerBackgroundRefresh(ctx, tenantID, streamID, fetch)
		return row.entry, nil
	}

	// Cold path: synchronous fetch.
	fresh, err := fetch(ctx, tenantID, streamID)
	if err != nil {
		// If there is a hard-expired or under-watermark entry, returning it
		// would violate the hard-TTL or revocation guarantee. Fail closed.
		return Entry{}, errors.Join(ErrFetchFailed, err)
	}
	c.putEntry(key, fresh)
	return fresh, nil
}

// BumpWatermark sets the minimum acceptable bundle_version for the
// (tenantID, streamID) key. Cached entries with BundleVersion < minVersion
// will not be served. This is the revocation channel: a 'bundle_revoke'
// outbox entry from Commodore translates to one BumpWatermark call.
//
// Bumping with a value <= current watermark is a no-op.
func (c *Cache) BumpWatermark(tenantID, streamID string, minVersion int64) {
	key := cacheKey(tenantID, streamID)
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.entries[key]
	if row == nil {
		c.entries[key] = &cacheRow{watermark: minVersion}
		return
	}
	if minVersion > row.watermark {
		row.watermark = minVersion
	}
}

// Watermark returns the current minimum-acceptable bundle_version for the
// key. Useful for tests and observability.
func (c *Cache) Watermark(tenantID, streamID string) int64 {
	key := cacheKey(tenantID, streamID)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if row := c.entries[key]; row != nil {
		return row.watermark
	}
	return 0
}

// Peek returns the cached entry without triggering refresh. Returns
// (zero, false) when no entry exists.
func (c *Cache) Peek(tenantID, streamID string) (Entry, bool) {
	key := cacheKey(tenantID, streamID)
	c.mu.RLock()
	defer c.mu.RUnlock()
	row := c.entries[key]
	if row == nil || row.entry.BundleJWT == "" {
		return Entry{}, false
	}
	return row.entry, true
}

func (c *Cache) putEntry(key string, e Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row := c.entries[key]
	if row == nil {
		c.entries[key] = &cacheRow{entry: e}
		return
	}
	row.entry = e
}

func (c *Cache) triggerBackgroundRefresh(parent context.Context, tenantID, streamID string, fetch FetchFunc) {
	key := cacheKey(tenantID, streamID)
	flag, _ := c.inflight.LoadOrStore(key, &atomic.Bool{})
	bf := flag.(*atomic.Bool) //nolint:errcheck // we just stored it
	if !bf.CompareAndSwap(false, true) {
		// A refresh is already in flight for this key; skip.
		return
	}
	// Use background context so a request-scoped cancellation does not
	// abort the refresh other readers are relying on.
	go func() {
		defer bf.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		fresh, err := fetch(ctx, tenantID, streamID)
		if err != nil {
			return
		}
		_ = parent // parent retained for future tracing propagation
		c.putEntry(key, fresh)
	}()
}
