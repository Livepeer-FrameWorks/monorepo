package state

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const (
	// Helmsman emits Mist's current client inventory every ten seconds. Viewer
	// leases survive several missed polls but recover promptly after a missing
	// USER_END or a dead media process.
	tenantViewerCapacityLease = 2 * time.Minute
	// Correlation outlives active capacity. It lets a Foghorn restart or a
	// delayed inventory sample reconstruct the fwcid-backed reservation from
	// the Mist session ID even after the active lease has lapsed.
	tenantViewerCorrelationRetention = 24 * time.Hour
	tenantViewerLocalPruneInterval   = time.Minute
)

type viewerCapacityLease struct {
	capacityID string
	expiresAt  time.Time
	retainTill time.Time
}

// TenantCapacityManager is the shared admission authority for per-tenant
// concurrent viewer limits.
//
// Viewer reservations persist the mapping Mist's USER_END does not carry:
// (node, session_id) -> fwcid-preferred capacity ID. Redis is
// authoritative under HA; the local representation supplies the same contract
// for single-process deployments and tests.
type TenantCapacityManager struct {
	mu sync.RWMutex

	viewers         map[string]map[string]viewerCapacityLease // tenant -> node/session -> lease
	lastViewerPrune time.Time

	redis     goredis.UniversalClient
	clusterID string
	now       func() time.Time
}

func NewTenantCapacityManager() *TenantCapacityManager {
	return &TenantCapacityManager{
		viewers: make(map[string]map[string]viewerCapacityLease),
		now:     time.Now,
	}
}

func (m *TenantCapacityManager) EnableRedisSync(client goredis.UniversalClient, clusterID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.redis = client
	m.clusterID = clusterID
}

func (m *TenantCapacityManager) key(kind, tenantID string) string {
	return fmt.Sprintf("{%s}:tenant_capacity:%s:%s", m.clusterID, tenantID, kind)
}

func viewerSessionField(nodeID, sessionID string) string {
	return strings.TrimSpace(nodeID) + "\x1f" + strings.TrimSpace(sessionID)
}

func redisCapacityCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 250*time.Millisecond)
}

// Lua concatenates the current millisecond timestamp only to form an exclusive
// sorted-set range bound. It is neither persisted as an authority identifier nor
// compared outside this script, and current epoch milliseconds remain exact in
// Lua's IEEE-754 integer range.
var reserveTenantViewer = goredis.NewScript(`
local now = tonumber(ARGV[4])
local function expireSession(field)
  local score = redis.call('ZSCORE', KEYS[2], field)
  if score and tonumber(score) <= now then
    local staleCapacity = redis.call('HGET', KEYS[3], field)
    if staleCapacity then
      local refs = redis.call('HINCRBY', KEYS[4], staleCapacity, -1)
      if refs <= 0 then
        redis.call('HDEL', KEYS[4], staleCapacity)
        redis.call('ZREM', KEYS[1], staleCapacity)
      end
    end
    redis.call('ZREM', KEYS[2], field)
  end
end
expireSession(ARGV[1])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, 128)
for _, field in ipairs(expired) do
  expireSession(field)
end
local forgotten = redis.call('ZRANGEBYSCORE', KEYS[5], '-inf', now, 'LIMIT', 0, 128)
for _, field in ipairs(forgotten) do
  if not redis.call('ZSCORE', KEYS[2], field) then
    redis.call('HDEL', KEYS[3], field)
    redis.call('ZREM', KEYS[5], field)
  end
end
local sessionScore = redis.call('ZSCORE', KEYS[2], ARGV[1])
local oldCapacity = redis.call('HGET', KEYS[3], ARGV[1])
if sessionScore and tonumber(sessionScore) > now and oldCapacity == ARGV[2] then
  redis.call('ZADD', KEYS[1], ARGV[5], ARGV[2])
  redis.call('ZADD', KEYS[2], ARGV[5], ARGV[1])
  redis.call('ZADD', KEYS[5], ARGV[6], ARGV[1])
  for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[7]) end
  return {1, 0, redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')}
end
local capacityScore = redis.call('ZSCORE', KEYS[1], ARGV[2])
local capacityActive = capacityScore and tonumber(capacityScore) > now
local count = redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')
local replacingSoleCapacity = sessionScore and tonumber(sessionScore) > now and oldCapacity and oldCapacity ~= ARGV[2]
  and tonumber(redis.call('HGET', KEYS[4], oldCapacity) or '0') <= 1
local effectiveCount = count
if replacingSoleCapacity then effectiveCount = effectiveCount - 1 end
if not capacityActive and effectiveCount >= tonumber(ARGV[3]) then
  for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[7]) end
  return {0, 0, count}
end
if sessionScore and tonumber(sessionScore) > now and oldCapacity and oldCapacity ~= ARGV[2] then
  local refs = redis.call('HINCRBY', KEYS[4], oldCapacity, -1)
  if refs <= 0 then
    redis.call('HDEL', KEYS[4], oldCapacity)
    redis.call('ZREM', KEYS[1], oldCapacity)
    count = count - 1
  end
end
redis.call('HINCRBY', KEYS[4], ARGV[2], 1)
redis.call('HSET', KEYS[3], ARGV[1], ARGV[2])
redis.call('ZADD', KEYS[1], ARGV[5], ARGV[2])
redis.call('ZADD', KEYS[2], ARGV[5], ARGV[1])
redis.call('ZADD', KEYS[5], ARGV[6], ARGV[1])
if not capacityActive then count = count + 1 end
for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[7]) end
return {1, 1, count}
`)

var renewTenantViewer = goredis.NewScript(`
local now = tonumber(ARGV[2])
local function expireSession(field)
  local score = redis.call('ZSCORE', KEYS[2], field)
  if score and tonumber(score) <= now then
    local staleCapacity = redis.call('HGET', KEYS[3], field)
    if staleCapacity then
      local refs = redis.call('HINCRBY', KEYS[4], staleCapacity, -1)
      if refs <= 0 then
        redis.call('HDEL', KEYS[4], staleCapacity)
        redis.call('ZREM', KEYS[1], staleCapacity)
      end
    end
    redis.call('ZREM', KEYS[2], field)
  end
end
expireSession(ARGV[1])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, 128)
for _, field in ipairs(expired) do
  expireSession(field)
end
local retained = redis.call('ZSCORE', KEYS[5], ARGV[1])
if not retained or tonumber(retained) <= now then
  local sessionScore = redis.call('ZSCORE', KEYS[2], ARGV[1])
  local capacity = redis.call('HGET', KEYS[3], ARGV[1])
  if sessionScore and capacity then
    local refs = redis.call('HINCRBY', KEYS[4], capacity, -1)
    if refs <= 0 then
      redis.call('HDEL', KEYS[4], capacity)
      redis.call('ZREM', KEYS[1], capacity)
    end
  end
  redis.call('ZREM', KEYS[2], ARGV[1])
  redis.call('HDEL', KEYS[3], ARGV[1])
  redis.call('ZREM', KEYS[5], ARGV[1])
  for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[5]) end
  return 0
end
local capacity = redis.call('HGET', KEYS[3], ARGV[1])
if not capacity then return 0 end
local sessionScore = redis.call('ZSCORE', KEYS[2], ARGV[1])
if not sessionScore or tonumber(sessionScore) <= now then
  local capacityScore = redis.call('ZSCORE', KEYS[1], capacity)
  local maxViewers = tonumber(ARGV[6])
  if (not capacityScore or tonumber(capacityScore) <= now) and maxViewers > 0 then
    local activeCount = redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')
    if activeCount >= maxViewers then return 0 end
  end
  redis.call('HINCRBY', KEYS[4], capacity, 1)
end
redis.call('ZADD', KEYS[1], ARGV[3], capacity)
redis.call('ZADD', KEYS[2], ARGV[3], ARGV[1])
redis.call('ZADD', KEYS[5], ARGV[4], ARGV[1])
for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[5]) end
return 1
`)

var releaseTenantViewer = goredis.NewScript(`
local now = tonumber(ARGV[2])
local function expireSession(field)
  local score = redis.call('ZSCORE', KEYS[2], field)
  if score and tonumber(score) <= now then
    local staleCapacity = redis.call('HGET', KEYS[3], field)
    if staleCapacity then
      local refs = redis.call('HINCRBY', KEYS[4], staleCapacity, -1)
      if refs <= 0 then
        redis.call('HDEL', KEYS[4], staleCapacity)
        redis.call('ZREM', KEYS[1], staleCapacity)
      end
    end
    redis.call('ZREM', KEYS[2], field)
  end
end
expireSession(ARGV[1])
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now, 'LIMIT', 0, 128)
for _, field in ipairs(expired) do
  expireSession(field)
end
local capacity = redis.call('HGET', KEYS[3], ARGV[1])
if not capacity then
  for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[3]) end
  return {'', 0, redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')}
end
local sessionScore = redis.call('ZSCORE', KEYS[2], ARGV[1])
local released = 0
if sessionScore and tonumber(sessionScore) > now then
  released = 1
  local refs = redis.call('HINCRBY', KEYS[4], capacity, -1)
  if refs <= 0 then
    redis.call('HDEL', KEYS[4], capacity)
    redis.call('ZREM', KEYS[1], capacity)
  end
end
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('HDEL', KEYS[3], ARGV[1])
redis.call('ZREM', KEYS[5], ARGV[1])
for i = 1, 5 do redis.call('PEXPIRE', KEYS[i], ARGV[3]) end
return {capacity, released, redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf')}
`)

func (m *TenantCapacityManager) viewerKeys(tenantID string) []string {
	return []string{
		m.key("viewers", tenantID), m.key("viewer_sessions", tenantID),
		m.key("viewer_correlations", tenantID), m.key("viewer_refs", tenantID),
		m.key("viewer_correlation_expiry", tenantID),
	}
}

// TryRegisterViewer reserves one logical viewer while durably binding the Mist
// session used by USER_END to the fwcid-preferred capacity ID used by USER_NEW.
func (m *TenantCapacityManager) TryRegisterViewer(tenantID, nodeID, sessionID, capacityID string, maxViewers int32) (allowed, added bool, count int, err error) {
	tenantID, sessionID, capacityID = strings.TrimSpace(tenantID), strings.TrimSpace(sessionID), strings.TrimSpace(capacityID)
	if tenantID == "" || sessionID == "" || capacityID == "" || maxViewers <= 0 {
		return false, false, 0, nil
	}
	field := viewerSessionField(nodeID, sessionID)
	now := m.now()
	expiresAt := now.Add(tenantViewerCapacityLease)
	retainTill := now.Add(tenantViewerCorrelationRetention)
	m.mu.RLock()
	r := m.redis
	m.mu.RUnlock()
	if r != nil {
		ctx, cancel := redisCapacityCtx()
		defer cancel()
		keyTTL := tenantViewerCorrelationRetention + tenantViewerCapacityLease
		result, runErr := reserveTenantViewer.Run(ctx, r, m.viewerKeys(tenantID), field, capacityID, maxViewers, now.UnixMilli(), expiresAt.UnixMilli(), retainTill.UnixMilli(), keyTTL.Milliseconds()).Result()
		if runErr != nil {
			return false, false, 0, fmt.Errorf("reserve tenant viewer capacity: %w", runErr)
		}
		values, parseErr := redisInts(result, 3)
		if parseErr != nil {
			return false, false, 0, fmt.Errorf("reserve tenant viewer capacity: %w", parseErr)
		}
		allowed, added, count = values[0] == 1, values[1] == 1, int(values[2])
		if allowed {
			m.rememberViewer(tenantID, field, capacityID, expiresAt, retainTill)
		}
		return allowed, added, count, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocalLocked(now)
	set := m.viewers[tenantID]
	if set == nil {
		set = make(map[string]viewerCapacityLease)
		m.viewers[tenantID] = set
	}
	if existing, ok := set[field]; ok && existing.expiresAt.After(now) && existing.capacityID == capacityID {
		existing.expiresAt, existing.retainTill = expiresAt, retainTill
		set[field] = existing
		return true, false, len(localViewerCapacitySet(set, now)), nil
	}
	active := localViewerCapacitySetExcluding(set, now, field)
	if _, exists := active[capacityID]; !exists && int32(len(active)) >= maxViewers {
		return false, false, len(active), nil
	}
	set[field] = viewerCapacityLease{capacityID: capacityID, expiresAt: expiresAt, retainTill: retainTill}
	return true, true, len(localViewerCapacitySet(set, now)), nil
}

func (m *TenantCapacityManager) rememberViewer(tenantID, field, capacityID string, expiresAt, retainTill time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocalIfDueLocked(m.now())
	set := m.viewers[tenantID]
	if set == nil {
		set = make(map[string]viewerCapacityLease)
		m.viewers[tenantID] = set
	}
	set[field] = viewerCapacityLease{capacityID: capacityID, expiresAt: expiresAt, retainTill: retainTill}
}

// RenewViewerSession consumes Helmsman's current Mist client inventory. It can
// reactivate a known correlation after a Foghorn restart without requiring the
// original request URL or admitting a second logical viewer.
func (m *TenantCapacityManager) RenewViewerSession(tenantID, nodeID, sessionID string, maxViewers int32) error {
	tenantID, sessionID = strings.TrimSpace(tenantID), strings.TrimSpace(sessionID)
	if tenantID == "" || sessionID == "" {
		return nil
	}
	field := viewerSessionField(nodeID, sessionID)
	now := m.now()
	expiresAt := now.Add(tenantViewerCapacityLease)
	retainTill := now.Add(tenantViewerCorrelationRetention)
	m.mu.RLock()
	r := m.redis
	m.mu.RUnlock()
	if r != nil {
		ctx, cancel := redisCapacityCtx()
		defer cancel()
		keyTTL := tenantViewerCorrelationRetention + tenantViewerCapacityLease
		ok, err := renewTenantViewer.Run(ctx, r, m.viewerKeys(tenantID), field, now.UnixMilli(), expiresAt.UnixMilli(), retainTill.UnixMilli(), keyTTL.Milliseconds(), maxViewers).Int()
		if err != nil {
			return fmt.Errorf("renew tenant viewer capacity: %w", err)
		}
		if ok == 0 {
			m.pruneLocalIfDue(now)
			return nil
		}
		m.mu.Lock()
		m.pruneLocalIfDueLocked(now)
		if lease, exists := m.viewers[tenantID][field]; exists {
			lease.expiresAt, lease.retainTill = expiresAt, retainTill
			m.viewers[tenantID][field] = lease
		}
		m.mu.Unlock()
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.viewers[tenantID][field]
	if !ok || !lease.retainTill.After(now) {
		return nil
	}
	if !lease.expiresAt.After(now) {
		active := localViewerCapacitySetExcluding(m.viewers[tenantID], now, field)
		if _, alreadyActive := active[lease.capacityID]; !alreadyActive && maxViewers > 0 && int32(len(active)) >= maxViewers {
			return nil
		}
	}
	lease.expiresAt, lease.retainTill = expiresAt, retainTill
	m.viewers[tenantID][field] = lease
	return nil
}

// ReleaseViewerSession resolves the durable correlation and releases the
// logical capacity member only when the last active Mist session using it is
// gone. The caller never needs the original fwcid-bearing request URL.
func (m *TenantCapacityManager) ReleaseViewerSession(tenantID, nodeID, sessionID string) (capacityID string, released bool, count int, err error) {
	tenantID, sessionID = strings.TrimSpace(tenantID), strings.TrimSpace(sessionID)
	if tenantID == "" || sessionID == "" {
		return "", false, 0, nil
	}
	field := viewerSessionField(nodeID, sessionID)
	now := m.now()
	m.mu.RLock()
	r := m.redis
	m.mu.RUnlock()
	if r != nil {
		ctx, cancel := redisCapacityCtx()
		defer cancel()
		keyTTL := tenantViewerCorrelationRetention + tenantViewerCapacityLease
		result, runErr := releaseTenantViewer.Run(ctx, r, m.viewerKeys(tenantID), field, now.UnixMilli(), keyTTL.Milliseconds()).Result()
		if runErr != nil {
			return "", false, 0, fmt.Errorf("release tenant viewer capacity: %w", runErr)
		}
		values, ok := result.([]any)
		if !ok || len(values) != 3 {
			return "", false, 0, fmt.Errorf("release tenant viewer capacity: malformed Redis result %T", result)
		}
		var idOK bool
		capacityID, idOK = values[0].(string)
		if !idOK {
			return "", false, 0, fmt.Errorf("release tenant viewer capacity: malformed capacity id %T", values[0])
		}
		numbers, parseErr := redisInts([]any{values[1], values[2]}, 2)
		if parseErr != nil {
			return "", false, 0, fmt.Errorf("release tenant viewer capacity: %w", parseErr)
		}
		released, count = numbers[0] == 1, int(numbers[1])
		m.mu.Lock()
		delete(m.viewers[tenantID], field)
		if len(m.viewers[tenantID]) == 0 {
			delete(m.viewers, tenantID)
		}
		m.mu.Unlock()
		return capacityID, released, count, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lease, ok := m.viewers[tenantID][field]
	if !ok {
		return "", false, len(localViewerCapacitySet(m.viewers[tenantID], now)), nil
	}
	delete(m.viewers[tenantID], field)
	count = len(localViewerCapacitySet(m.viewers[tenantID], now))
	if len(m.viewers[tenantID]) == 0 {
		delete(m.viewers, tenantID)
	}
	return lease.capacityID, lease.expiresAt.After(now), count, nil
}

func (m *TenantCapacityManager) CountViewers(tenantID string) int {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return 0
	}
	now := m.now()
	m.mu.RLock()
	r := m.redis
	m.mu.RUnlock()
	if r != nil {
		ctx, cancel := redisCapacityCtx()
		defer cancel()
		count, err := r.ZCount(ctx, m.key("viewers", tenantID), "("+strconv.FormatInt(now.UnixMilli(), 10), "+inf").Result()
		m.pruneLocalIfDue(now)
		if err == nil {
			return int(count)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocalLocked(now)
	return len(localViewerCapacitySet(m.viewers[tenantID], now))
}

func (m *TenantCapacityManager) HasViewer(tenantID, capacityID string) bool {
	tenantID, capacityID = strings.TrimSpace(tenantID), strings.TrimSpace(capacityID)
	if tenantID == "" || capacityID == "" {
		return false
	}
	now := m.now()
	m.mu.RLock()
	r := m.redis
	m.mu.RUnlock()
	if r != nil {
		ctx, cancel := redisCapacityCtx()
		defer cancel()
		score, err := r.ZScore(ctx, m.key("viewers", tenantID), capacityID).Result()
		m.pruneLocalIfDue(now)
		return err == nil && score > float64(now.UnixMilli())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocalLocked(now)
	_, ok := localViewerCapacitySet(m.viewers[tenantID], now)[capacityID]
	return ok
}

func localViewerCapacitySet(set map[string]viewerCapacityLease, now time.Time) map[string]struct{} {
	return localViewerCapacitySetExcluding(set, now, "")
}

func localViewerCapacitySetExcluding(set map[string]viewerCapacityLease, now time.Time, excludedField string) map[string]struct{} {
	active := make(map[string]struct{})
	for field, lease := range set {
		if field == excludedField {
			continue
		}
		if lease.expiresAt.After(now) {
			active[lease.capacityID] = struct{}{}
		}
	}
	return active
}

func (m *TenantCapacityManager) pruneLocalLocked(now time.Time) {
	for tenantID, set := range m.viewers {
		for field, lease := range set {
			if !lease.retainTill.After(now) {
				delete(set, field)
			}
		}
		if len(set) == 0 {
			delete(m.viewers, tenantID)
		}
	}
	m.lastViewerPrune = now
}

func (m *TenantCapacityManager) pruneLocalIfDue(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocalIfDueLocked(now)
}

func (m *TenantCapacityManager) pruneLocalIfDueLocked(now time.Time) {
	if !m.lastViewerPrune.IsZero() && now.Sub(m.lastViewerPrune) < tenantViewerLocalPruneInterval {
		return
	}
	m.pruneLocalLocked(now)
}

func redisInts(result any, want int) ([]int64, error) {
	values, ok := result.([]any)
	if !ok || len(values) != want {
		return nil, fmt.Errorf("unexpected Redis result %T", result)
	}
	out := make([]int64, len(values))
	for i, value := range values {
		switch typed := value.(type) {
		case int64:
			out[i] = typed
		case int:
			out[i] = int64(typed)
		default:
			return nil, fmt.Errorf("malformed Redis result value %T", value)
		}
	}
	return out, nil
}

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
