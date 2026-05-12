package state

import (
	"context"
	"fmt"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// TenantCapacityManager tracks per-tenant concurrent stream and viewer counts
// for runtime fair-use enforcement. Counts are kept as sets keyed by stable
// identifiers (internal_name for streams, session_id for viewers) so duplicate
// trigger fires are idempotent — Mist can re-fire PUSH_REWRITE on retry or
// USER_NEW on reconnect without inflating the count.
//
// When EnableRedisSync is called, sets are mirrored to Redis (SADD/SREM)
// and counts are sourced from Redis (SCARD) so multiple Foghorn instances
// in an HA pool agree on cluster-wide counts. Local in-memory state remains
// for fast HasStream/HasViewer probes and for fallback when Redis is
// unreachable. Single-instance Foghorn (today's prod) works correctly with
// in-memory only.
type TenantCapacityManager struct {
	mu      sync.RWMutex
	streams map[string]map[string]struct{} // tenant_id -> set(internal_name)
	viewers map[string]map[string]struct{} // tenant_id -> set(session_id)

	redis     goredis.UniversalClient
	clusterID string
}

const tenantViewerSetTTL = 2 * time.Hour

func NewTenantCapacityManager() *TenantCapacityManager {
	return &TenantCapacityManager{
		streams: make(map[string]map[string]struct{}),
		viewers: make(map[string]map[string]struct{}),
	}
}

// EnableRedisSync attaches a Redis client so cluster-wide counts agree
// across multiple Foghorn instances. clusterID partitions keys so two
// clusters sharing a Redis don't collide. Safe to call once at startup.
func (m *TenantCapacityManager) EnableRedisSync(client goredis.UniversalClient, clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.redis = client
	m.clusterID = clusterID
}

func (m *TenantCapacityManager) keyStreams(tenantID string) string {
	return fmt.Sprintf("{%s}:tenant_streams:%s", m.clusterID, tenantID)
}

func (m *TenantCapacityManager) keyViewers(tenantID string) string {
	return fmt.Sprintf("{%s}:tenant_viewers:%s", m.clusterID, tenantID)
}

// redisCtx returns a short-bounded context for Redis ops. Cap check is on
// the hot trigger path; we do not want to block trigger processing on a
// slow Redis. On timeout/error the caller falls through to the local
// count, which is still reasonable for single-instance deploys and a
// best-effort for HA.
func redisCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 250*time.Millisecond)
}

// RegisterStream records an active stream for a tenant. Idempotent — the
// same (tenant, internal_name) added twice counts once. Returns the count
// after the operation (local view; Redis count may differ briefly under HA).
func (m *TenantCapacityManager) RegisterStream(tenantID, internalName string) int {
	if tenantID == "" || internalName == "" {
		return 0
	}
	m.mu.Lock()
	set := m.streams[tenantID]
	if set == nil {
		set = make(map[string]struct{})
		m.streams[tenantID] = set
	}
	set[internalName] = struct{}{}
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyStreams(tenantID)
	}
	count := len(set)
	m.mu.Unlock()
	if r != nil {
		ctx, cancel := redisCtx()
		defer cancel()
		// Best-effort: local state already updated; HA peers reconcile on
		// the next op. A failed SADD just means the cluster-wide count is
		// briefly behind reality, not that the trigger should fail.
		r.SAdd(ctx, key, internalName) //nolint:errcheck
	}
	return count
}

// ReconcileStreams replaces the tenant's active-stream set with the stream
// manager's current live-stream view. STREAM_END is best-effort; this prevents
// a missed end event from permanently consuming a slot in Redis-backed HA mode.
func (m *TenantCapacityManager) ReconcileStreams(tenantID string, activeInternalNames []string) int {
	if tenantID == "" {
		return 0
	}
	next := make(map[string]struct{}, len(activeInternalNames))
	for _, name := range activeInternalNames {
		if name != "" {
			next[name] = struct{}{}
		}
	}
	m.mu.Lock()
	if len(next) == 0 {
		delete(m.streams, tenantID)
	} else {
		m.streams[tenantID] = next
	}
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyStreams(tenantID)
	}
	count := len(next)
	m.mu.Unlock()
	if r != nil {
		ctx, cancel := redisCtx()
		defer cancel()
		if count == 0 {
			r.Del(ctx, key) //nolint:errcheck
			return 0
		}
		members := make([]any, 0, count)
		for name := range next {
			members = append(members, name)
		}
		pipe := r.Pipeline()
		pipe.Del(ctx, key)
		pipe.SAdd(ctx, key, members...)
		if _, err := pipe.Exec(ctx); err != nil {
			return count
		}
	}
	return count
}

// UnregisterStream removes an active stream. Safe to call for unknown streams
// (no-op). Returns the count after the operation.
func (m *TenantCapacityManager) UnregisterStream(tenantID, internalName string) int {
	if tenantID == "" || internalName == "" {
		return 0
	}
	m.mu.Lock()
	set := m.streams[tenantID]
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyStreams(tenantID)
	}
	count := 0
	if set != nil {
		delete(set, internalName)
		if len(set) == 0 {
			delete(m.streams, tenantID)
		} else {
			count = len(set)
		}
	}
	m.mu.Unlock()
	if r != nil {
		ctx, cancel := redisCtx()
		defer cancel()
		// Best-effort: a failed SREM is recoverable on the next op.
		r.SRem(ctx, key, internalName) //nolint:errcheck
	}
	return count
}

// CountStreams returns the current concurrent stream count for a tenant.
// When Redis sync is enabled, sources from SCARD (cluster-wide truth);
// otherwise from local memory. Falls back to local on Redis error.
func (m *TenantCapacityManager) CountStreams(tenantID string) int {
	if tenantID == "" {
		return 0
	}
	m.mu.RLock()
	local := len(m.streams[tenantID])
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyStreams(tenantID)
	}
	m.mu.RUnlock()
	if r == nil {
		return local
	}
	ctx, cancel := redisCtx()
	defer cancel()
	n, err := r.SCard(ctx, key).Result()
	if err != nil {
		return local
	}
	return int(n)
}

// HasStream reports whether (tenant, internal_name) is currently registered.
// Used so trigger handlers can distinguish a re-fire of an already-tracked
// stream (admit, since count doesn't go up) from a genuine new stream that
// would push the tenant over their cap. Sources from Redis when enabled.
func (m *TenantCapacityManager) HasStream(tenantID, internalName string) bool {
	if tenantID == "" || internalName == "" {
		return false
	}
	m.mu.RLock()
	_, local := m.streams[tenantID][internalName]
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyStreams(tenantID)
	}
	m.mu.RUnlock()
	if r == nil {
		return local
	}
	ctx, cancel := redisCtx()
	defer cancel()
	present, err := r.SIsMember(ctx, key, internalName).Result()
	if err != nil {
		return local
	}
	return present
}

// RegisterViewer records an active viewer session for a tenant. Idempotent.
func (m *TenantCapacityManager) RegisterViewer(tenantID, sessionID string) int {
	if tenantID == "" || sessionID == "" {
		return 0
	}
	m.mu.Lock()
	set := m.viewers[tenantID]
	if set == nil {
		set = make(map[string]struct{})
		m.viewers[tenantID] = set
	}
	set[sessionID] = struct{}{}
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyViewers(tenantID)
	}
	count := len(set)
	m.mu.Unlock()
	if r != nil {
		ctx, cancel := redisCtx()
		defer cancel()
		// Best-effort viewer SADD; see comment on stream SADD above.
		pipe := r.Pipeline()
		pipe.SAdd(ctx, key, sessionID)
		pipe.Expire(ctx, key, tenantViewerSetTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return count
		}
	}
	return count
}

// UnregisterViewer removes an active viewer.
func (m *TenantCapacityManager) UnregisterViewer(tenantID, sessionID string) int {
	if tenantID == "" || sessionID == "" {
		return 0
	}
	m.mu.Lock()
	set := m.viewers[tenantID]
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyViewers(tenantID)
	}
	count := 0
	if set != nil {
		delete(set, sessionID)
		if len(set) == 0 {
			delete(m.viewers, tenantID)
		} else {
			count = len(set)
		}
	}
	m.mu.Unlock()
	if r != nil {
		ctx, cancel := redisCtx()
		defer cancel()
		// Best-effort viewer SREM.
		r.SRem(ctx, key, sessionID) //nolint:errcheck
	}
	return count
}

// CountViewers returns the current concurrent viewer count for a tenant.
func (m *TenantCapacityManager) CountViewers(tenantID string) int {
	if tenantID == "" {
		return 0
	}
	m.mu.RLock()
	local := len(m.viewers[tenantID])
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyViewers(tenantID)
	}
	m.mu.RUnlock()
	if r == nil {
		return local
	}
	ctx, cancel := redisCtx()
	defer cancel()
	n, err := r.SCard(ctx, key).Result()
	if err != nil {
		return local
	}
	return int(n)
}

// HasViewer reports whether (tenant, session_id) is currently registered.
func (m *TenantCapacityManager) HasViewer(tenantID, sessionID string) bool {
	if tenantID == "" || sessionID == "" {
		return false
	}
	m.mu.RLock()
	_, local := m.viewers[tenantID][sessionID]
	r := m.redis
	key := ""
	if r != nil {
		key = m.keyViewers(tenantID)
	}
	m.mu.RUnlock()
	if r == nil {
		return local
	}
	ctx, cancel := redisCtx()
	defer cancel()
	present, err := r.SIsMember(ctx, key, sessionID).Result()
	if err != nil {
		return local
	}
	return present
}

// Package-level default manager for trigger-handler convenience. Tests can
// reset via ResetDefaultTenantCapacityForTests.
var (
	defaultTenantCapacity   *TenantCapacityManager
	defaultTenantCapacityMu sync.Mutex
)

func DefaultTenantCapacity() *TenantCapacityManager {
	defaultTenantCapacityMu.Lock()
	defer defaultTenantCapacityMu.Unlock()
	if defaultTenantCapacity == nil {
		defaultTenantCapacity = NewTenantCapacityManager()
	}
	return defaultTenantCapacity
}

func ResetDefaultTenantCapacityForTests() *TenantCapacityManager {
	defaultTenantCapacityMu.Lock()
	defer defaultTenantCapacityMu.Unlock()
	defaultTenantCapacity = NewTenantCapacityManager()
	return defaultTenantCapacity
}
