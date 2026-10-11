package mediaauthority

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

const (
	// placementPairCacheTTL bounds how long a replica answers from a pair it
	// read before another replica of the cell applied a change: every answer
	// comes from a database read begun less than this long before the request.
	// Revocations applied on another replica therefore take effect here within
	// it even when the cell's changelog announcement is lost.
	placementPairCacheTTL        = time.Second
	placementPairCacheMaxEntries = 65_536
)

// PlacementPairReadResults are the result labels of placement_pair_reads_total:
// hit (answered from a kept pair or by a read already in flight), miss (read
// from the database and kept), bypass (read from the database and not kept:
// absent, failed, hard-expired, or changed while it was read).
var PlacementPairReadResults = []string{"hit", "miss", "bypass"}

// pairCache keeps decoded placement pairs for placementPairCacheTTL and
// collapses concurrent reads of one pair into one database read.
//
// Only the stored rows are kept. Freshness, the restore fence's withholding
// and the soft-expiry refresh are evaluated by each caller at use, so a kept
// pair that passes refresh_after or valid_until is reported as such at once.
// A pair that has expired at use is not answered from the cache.
type pairCache struct {
	mu        sync.Mutex
	tenants   map[string]*pairCacheTenant
	entries   int
	lastSweep time.Time
	reads     *prometheus.CounterVec
}

type pairCacheTenant struct {
	entries map[string]*pairCacheEntry  // by internal name
	flights map[string]*pairCacheFlight // by flight key
}

// storedPlacementPair is a pair as the database holds it, before freshness.
type storedPlacementPair struct {
	pair PlacementPair
	// objectWithheld is the row's tenant-revival withholding of the object.
	objectWithheld bool
}

type pairCacheEntry struct {
	stored     storedPlacementPair
	loadedAt   time.Time
	fenceEpoch uint64
}

type pairCacheFlight struct {
	internalName string
	startedAt    time.Time
	fenceEpoch   uint64
	done         chan struct{}
	// stored, err and kept are written once before done is closed.
	stored storedPlacementPair
	err    error
	kept   bool
	// detached is guarded by pairCache.mu: an invalidation or a newer read
	// replaced this flight, so its result answers only the callers already
	// waiting and is never kept.
	detached bool
}

func newPairCache() *pairCache {
	return &pairCache{tenants: map[string]*pairCacheTenant{}}
}

// SetPlacementPairReadMetric installs the placement pair read counter.
func (s *Store) SetPlacementPairReadMetric(counter *prometheus.CounterVec) {
	if s != nil && s.pairs != nil {
		s.pairs.mu.Lock()
		s.pairs.reads = counter
		s.pairs.mu.Unlock()
	}
}

// ForgetPlacementPairs drops the kept placement pairs an authority change
// touches: one media object of a tenant (by internal name), all of a tenant's
// pairs for a tenant change, and every pair when the tenant is unknown. It
// serves announcements from the cell's other replicas and must not block.
func (s *Store) ForgetPlacementPairs(kind, tenantID, internalName string) {
	if s == nil || s.pairs == nil {
		return
	}
	switch {
	case tenantID == "":
		s.pairs.forgetAll()
	case kind == "media_object" && internalName != "":
		s.pairs.forget(tenantID, internalName, "")
	default:
		s.pairs.forget(tenantID, "", "")
	}
}

// forgetApplied drops what a committed apply may have changed. A tenant apply
// also rewrites the readiness and withholding of the tenant's objects.
func (s *Store) forgetApplied(result ApplyResult) {
	if s == nil || s.pairs == nil {
		return
	}
	if result.Kind == "media_object" && result.TenantID != "" && (result.InternalName != "" || result.ID != "") {
		s.pairs.forget(result.TenantID, result.InternalName, result.ID)
		return
	}
	tenantID := result.TenantID
	if tenantID == "" && result.Kind == "tenant" {
		tenantID = result.ID
	}
	if tenantID == "" {
		s.pairs.forgetAll()
		return
	}
	s.pairs.forget(tenantID, "", "")
}

// forgetLocalReadiness drops kept pairs after this replica marked readiness:
// one object's, or all of a tenant's when the tenant row changed too.
func (s *Store) forgetLocalReadiness(tenantID, authorityID string) {
	if s == nil || s.pairs == nil {
		return
	}
	s.pairs.forget(strings.TrimSpace(tenantID), "", strings.TrimSpace(authorityID))
}

func (c *pairCache) count(result string) {
	if c.reads != nil {
		c.reads.WithLabelValues(result).Inc()
	}
}

// forget drops one object's kept pair and in-flight reads (by internal name or
// authority ID), or all of a tenant's when both are empty.
func (c *pairCache) forget(tenantID, internalName, authorityID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tenant := c.tenants[tenantID]
	if tenant == nil {
		return
	}
	if internalName == "" && authorityID == "" {
		for _, flight := range tenant.flights {
			flight.detached = true
		}
		c.entries -= len(tenant.entries)
		delete(c.tenants, tenantID)
		return
	}
	for name, entry := range tenant.entries {
		if name == internalName || (authorityID != "" && entry.stored.pair.Object.AuthorityID == authorityID) {
			delete(tenant.entries, name)
			c.entries--
		}
	}
	for key, flight := range tenant.flights {
		if flight.internalName == internalName || (authorityID != "" && flightAuthorityID(key) == authorityID) {
			flight.detached = true
			delete(tenant.flights, key)
		}
	}
	c.pruneTenantLocked(tenantID, tenant)
}

func (c *pairCache) forgetAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tenant := range c.tenants {
		for _, flight := range tenant.flights {
			flight.detached = true
		}
	}
	c.tenants = map[string]*pairCacheTenant{}
	c.entries = 0
}

func (c *pairCache) tenantLocked(tenantID string) *pairCacheTenant {
	tenant := c.tenants[tenantID]
	if tenant == nil {
		tenant = &pairCacheTenant{entries: map[string]*pairCacheEntry{}, flights: map[string]*pairCacheFlight{}}
		c.tenants[tenantID] = tenant
	}
	return tenant
}

func (c *pairCache) pruneTenantLocked(tenantID string, tenant *pairCacheTenant) {
	if len(tenant.entries) == 0 && len(tenant.flights) == 0 && c.tenants[tenantID] == tenant {
		delete(c.tenants, tenantID)
	}
}

// sweepLocked drops entries past their lifetime, at most once per lifetime.
func (c *pairCache) sweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) < placementPairCacheTTL {
		return
	}
	c.lastSweep = now
	for tenantID, tenant := range c.tenants {
		for name, entry := range tenant.entries {
			if now.Sub(entry.loadedAt) >= placementPairCacheTTL {
				delete(tenant.entries, name)
				c.entries--
			}
		}
		c.pruneTenantLocked(tenantID, tenant)
	}
}

// pairFlightKey separates reads by authority identity from reads by internal
// name: they are different queries and fail differently.
func pairFlightKey(authorityID, internalName string) string {
	if authorityID == "" {
		return "n\x00" + internalName
	}
	return "a\x00" + authorityID + "\x00" + internalName
}

func flightAuthorityID(key string) string {
	if len(key) < 2 || key[0] != 'a' {
		return ""
	}
	rest := key[2:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == 0 {
			return rest[:i]
		}
	}
	return rest
}

// readPlacementPair answers one local pair read: from a kept pair, by joining
// a read of the same pair in flight, or by reading the database. authorityID
// is empty for a read by internal name. Every answer comes from a database
// read begun less than placementPairCacheTTL ago, in the current restore-fence
// epoch, after the last local apply or announced change touching the pair.
func (s *Store) readPlacementPair(ctx context.Context, tenantID, authorityID, internalName string, load func(context.Context) (storedPlacementPair, error)) (PlacementPair, error) {
	c := s.pairs
	if c == nil {
		stored, err := load(ctx)
		if err != nil {
			return PlacementPair{}, err
		}
		return s.placementAtUse(stored, s.now().UTC(), false), nil
	}
	now := s.now()
	epoch := s.fence.changeEpoch()

	c.mu.Lock()
	entry := c.tenantLocked(tenantID).entries[internalName]
	c.mu.Unlock()
	if entry != nil && entry.fenceEpoch == epoch && now.Sub(entry.loadedAt) < placementPairCacheTTL &&
		(authorityID == "" || entry.stored.pair.Object.AuthorityID == authorityID) {
		if s.usablePlacement(entry.stored, now.UTC()) {
			c.mu.Lock()
			c.count("hit")
			c.mu.Unlock()
			return s.placementAtUse(entry.stored, now.UTC(), true), nil
		}
	}

	key := pairFlightKey(authorityID, internalName)
	c.mu.Lock()
	tenant := c.tenantLocked(tenantID)
	// The kept pair could not answer: it is too old, from another fence epoch,
	// expired at use, or for another authority under this name.
	if entry != nil && tenant.entries[internalName] == entry {
		delete(tenant.entries, internalName)
		c.entries--
	}
	flight := tenant.flights[key]
	if flight != nil && (flight.fenceEpoch != epoch || now.Sub(flight.startedAt) >= placementPairCacheTTL) {
		flight.detached = true
		delete(tenant.flights, key)
		flight = nil
	}
	leader := flight == nil
	if leader {
		flight = &pairCacheFlight{internalName: internalName, startedAt: now, fenceEpoch: epoch, done: make(chan struct{})}
		tenant.flights[key] = flight
	}
	c.mu.Unlock()

	if leader {
		// The read outlives a caller that gives up, so callers sharing it are
		// not failed by the first caller's deadline.
		go s.runPairFlight(context.WithoutCancel(ctx), tenantID, key, flight, load)
	}
	select {
	case <-flight.done:
	case <-ctx.Done():
		return PlacementPair{}, ctx.Err()
	}
	result := "hit"
	if leader {
		result = "bypass"
		if flight.kept {
			result = "miss"
		}
	}
	c.mu.Lock()
	c.count(result)
	c.mu.Unlock()
	if flight.err != nil {
		return PlacementPair{}, flight.err
	}
	return s.placementAtUse(flight.stored, s.now().UTC(), true), nil
}

func (s *Store) runPairFlight(ctx context.Context, tenantID, key string, flight *pairCacheFlight, load func(context.Context) (storedPlacementPair, error)) {
	c := s.pairs
	readCtx, cancel := context.WithTimeout(ctx, placementLocalReadTimeout)
	stored, err := load(readCtx)
	cancel()
	keep := false
	if err == nil {
		keep = s.usablePlacement(stored, s.now().UTC())
	}
	now := s.now()
	c.mu.Lock()
	// Sweep while this flight still holds its tenant's bucket in place. A
	// flight that is not detached is still in the bucket c.tenants holds.
	c.sweepLocked(now)
	tenant := c.tenants[tenantID]
	if tenant != nil && tenant.flights[key] == flight {
		delete(tenant.flights, key)
	}
	keep = keep && !flight.detached && tenant != nil && s.fence.changeEpoch() == flight.fenceEpoch &&
		stored.pair.Object.Authority.GetInternalName() == flight.internalName
	if keep {
		if _, replaced := tenant.entries[flight.internalName]; !replaced && c.entries >= placementPairCacheMaxEntries {
			keep = false
		} else {
			if !replaced {
				c.entries++
			}
			tenant.entries[flight.internalName] = &pairCacheEntry{stored: stored, loadedAt: flight.startedAt, fenceEpoch: flight.fenceEpoch}
		}
	}
	if tenant != nil {
		c.pruneTenantLocked(tenantID, tenant)
	}
	flight.stored, flight.err, flight.kept = stored, err, keep
	c.mu.Unlock()
	close(flight.done)
}

// usablePlacement reports, without side effects, whether a stored pair may be
// answered from the cache at now: neither half is hard-expired, which also
// covers the restore fence and tenant-revival withholding.
func (s *Store) usablePlacement(stored storedPlacementPair, now time.Time) bool {
	tenant, object := s.placementFreshness(stored, now)
	return tenant != FreshnessHardExpired && object != FreshnessHardExpired
}

func (s *Store) placementFreshness(stored storedPlacementPair, now time.Time) (tenant, object Freshness) {
	pair := stored.pair
	tenant = s.fencedFreshness(authorityFreshness(now, pair.Tenant.RefreshAfter, pair.Tenant.ValidUntil), "tenant", pair.Tenant.Authority.GetTenantId(), false)
	object = s.fencedFreshness(authorityFreshness(now, pair.Object.RefreshAfter, pair.Object.ValidUntil), "media_object", pair.Object.AuthorityID, stored.objectWithheld)
	return tenant, object
}

// placementAtUse is what one caller receives: freshness evaluated now, the
// soft-expiry refresh requested, and, for a shared pair, its own copy of the
// signed payloads.
func (s *Store) placementAtUse(stored storedPlacementPair, now time.Time, shared bool) PlacementPair {
	pair := stored.pair
	pair.Tenant.Freshness, pair.Object.Freshness = s.placementFreshness(stored, now)
	if shared {
		pair.Tenant.Authority = proto.CloneOf(pair.Tenant.Authority)
		pair.Object.Authority = proto.CloneOf(pair.Object.Authority)
	}
	s.observeFreshness(pair.Tenant.Freshness, AuthorityLookup{TenantID: pair.Tenant.Authority.GetTenantId()})
	s.observeFreshness(pair.Object.Freshness, AuthorityLookup{AuthorityID: pair.Object.AuthorityID})
	return pair
}
