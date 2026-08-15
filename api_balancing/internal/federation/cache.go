package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	goredis "github.com/redis/go-redis/v9"
)

// RemoteEdgeCache stores cross-cluster state in Redis for federation scoring.
// All keys use the {cluster_id}: hash-tag prefix so they slot together with
// the local state store (same pattern as state.RedisStateStore).
type RemoteEdgeCache struct {
	client    goredis.UniversalClient
	clusterID string
	logger    logging.Logger
}

// NewRemoteEdgeCache creates a cache backed by the given Redis client.
func NewRemoteEdgeCache(client goredis.UniversalClient, clusterID string, logger logging.Logger) *RemoteEdgeCache {
	return &RemoteEdgeCache{
		client:    client,
		clusterID: clusterID,
		logger:    logger,
	}
}

// TTLs for remote state. Short TTLs ensure stale data expires quickly when
// a PeerChannel drops or a replication ends.
const (
	remoteEdgeTTL         = 30 * time.Second
	remoteReplicationTTL  = 5 * time.Minute
	originPullLockTTL     = 15 * time.Second
	edgeSummaryTTL        = 60 * time.Second
	leaderLeaseTTL        = 15 * time.Second
	peerAddrTTL           = 30 * time.Second
	remoteLiveStreamTTL   = 30 * time.Second // refreshed every 5s by heartbeat
	remoteOfflineFenceTTL = time.Hour
	peerHeartbeatTTL      = 30 * time.Second // 3 missed 10s heartbeats = dead
)

// TryAcquireLeaderLease attempts to acquire a leader lease for the given role.
// Returns true if this instance is now the leader. Uses SET NX with TTL.
func (c *RemoteEdgeCache) TryAcquireLeaderLease(ctx context.Context, role, instanceID string) bool {
	key := fmt.Sprintf("{%s}:leader:%s", c.clusterID, role)
	ok, err := c.client.SetNX(ctx, key, instanceID, leaderLeaseTTL).Result()
	if err != nil {
		return false
	}
	if ok {
		return true
	}
	// Check if we already hold the lease (re-entrant)
	val, err := c.client.Get(ctx, key).Result()
	return err == nil && val == instanceID
}

// Lua scripts keep lease ownership verification and mutation atomic.
var renewLeaseScript = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('pexpire', KEYS[1], ARGV[2])
else
  return 0
end
`)

var releaseLeaseScript = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
else
  return 0
end
`)

// GetLeaderInstance returns the instance currently holding the role's leader lease ("" when none).
func (c *RemoteEdgeCache) GetLeaderInstance(ctx context.Context, role string) string {
	key := fmt.Sprintf("{%s}:leader:%s", c.clusterID, role)
	v, err := c.client.Get(ctx, key).Result()
	if err != nil {
		return ""
	}
	return v
}

// RenewLeaderLease atomically extends the TTL if we still hold the lease.
func (c *RemoteEdgeCache) RenewLeaderLease(ctx context.Context, role, instanceID string) bool {
	key := fmt.Sprintf("{%s}:leader:%s", c.clusterID, role)
	ttlMs := int64(leaderLeaseTTL / time.Millisecond)
	result, err := renewLeaseScript.Run(ctx, c.client, []string{key}, instanceID, ttlMs).Int64()
	return err == nil && result == 1
}

// ReleaseLeaderLease atomically releases the lease if we hold it.
func (c *RemoteEdgeCache) ReleaseLeaderLease(ctx context.Context, role, instanceID string) {
	key := fmt.Sprintf("{%s}:leader:%s", c.clusterID, role)
	releaseLeaseScript.Run(ctx, c.client, []string{key}, instanceID) //nolint:errcheck
}

// --- Key helpers ---

func (c *RemoteEdgeCache) keyRemoteEdge(peerClusterID, nodeID string) string {
	return fmt.Sprintf("{%s}:remote_edges:%s:%s", c.clusterID, peerClusterID, nodeID)
}

func (c *RemoteEdgeCache) keyRemoteEdgePattern(peerClusterID string) string {
	return fmt.Sprintf("{%s}:remote_edges:%s:*", c.clusterID, peerClusterID)
}

func (c *RemoteEdgeCache) keyRemoteReplication(streamName, peerClusterID string) string {
	return fmt.Sprintf("{%s}:remote_replications:%s:%s", c.clusterID, streamName, peerClusterID)
}

func (c *RemoteEdgeCache) keyRemoteReplicationPattern(streamName string) string {
	return fmt.Sprintf("{%s}:remote_replications:%s:*", c.clusterID, streamName)
}

func (c *RemoteEdgeCache) keyOriginPullLock(streamName string) string {
	return fmt.Sprintf("{%s}:origin_pull_lock:%s", c.clusterID, streamName)
}

func (c *RemoteEdgeCache) keyEdgeSummary(peerClusterID string) string {
	return fmt.Sprintf("{%s}:edge_summary:%s", c.clusterID, peerClusterID)
}

func (c *RemoteEdgeCache) keyPeerHintContribution(contributorID string) string {
	return fmt.Sprintf("{%s}:peer_hints:v2:%s", c.clusterID, contributorID)
}

func (c *RemoteEdgeCache) keyPeerHintContributionPattern() string {
	return fmt.Sprintf("{%s}:peer_hints:v2:*", c.clusterID)
}

func (c *RemoteEdgeCache) keyRemoteLiveStream(tenantID, internalName, originClusterID string) string {
	return fmt.Sprintf("{%s}:remote_live_streams:v3:records:%s:%s:%s", c.clusterID, tenantID, internalName, originClusterID)
}

func (c *RemoteEdgeCache) keyRemoteLiveStreamOrigins(tenantID, internalName string) string {
	return fmt.Sprintf("{%s}:remote_live_streams:v3:origins:%s:%s", c.clusterID, tenantID, internalName)
}

// --- Remote Edge Telemetry (per-node, per-peer, TTL 30s) ---

// RemoteEdgeEntry is the JSON representation stored in Redis for a single remote edge.
type RemoteEdgeEntry struct {
	StreamName  string  `json:"stream_name"`
	NodeID      string  `json:"node_id"`
	BaseURL     string  `json:"base_url"`
	BWAvailable uint64  `json:"bw_available"`
	ViewerCount uint32  `json:"viewer_count"`
	CPUPercent  float64 `json:"cpu_percent"`
	RAMUsed     uint64  `json:"ram_used"`
	RAMMax      uint64  `json:"ram_max"`
	GeoLat      float64 `json:"geo_lat"`
	GeoLon      float64 `json:"geo_lon"`
	UpdatedAt   int64   `json:"updated_at"`
}

// SetRemoteEdge writes a single remote edge's telemetry to Redis.
func (c *RemoteEdgeCache) SetRemoteEdge(ctx context.Context, peerClusterID string, entry *RemoteEdgeEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal remote edge: %w", err)
	}
	key := c.keyRemoteEdge(peerClusterID, entry.NodeID)
	return c.client.Set(ctx, key, data, remoteEdgeTTL).Err()
}

// GetRemoteEdges returns all cached remote edges for a given peer cluster.
func (c *RemoteEdgeCache) GetRemoteEdges(ctx context.Context, peerClusterID string) ([]*RemoteEdgeEntry, error) {
	pattern := c.keyRemoteEdgePattern(peerClusterID)
	return scanEntries[RemoteEdgeEntry](ctx, c.client, pattern)
}

// GetAllRemoteEdges returns all cached remote edges across all peer clusters.
func (c *RemoteEdgeCache) GetAllRemoteEdges(ctx context.Context) ([]*RemoteEdgeEntry, error) {
	pattern := fmt.Sprintf("{%s}:remote_edges:*", c.clusterID)
	return scanEntries[RemoteEdgeEntry](ctx, c.client, pattern)
}

// --- Remote Replication Events (per-stream, per-peer, TTL 5m) ---

// RemoteReplicationEntry records that a peer cluster has a stream available.
type RemoteReplicationEntry struct {
	StreamName string `json:"stream_name"`
	NodeID     string `json:"node_id"`
	ClusterID  string `json:"cluster_id"`
	BaseURL    string `json:"base_url"`
	DTSCURL    string `json:"dtsc_url"`
	Available  bool   `json:"available"`
	UpdatedAt  int64  `json:"updated_at"`
}

// SetRemoteReplication writes a replication event from a peer.
func (c *RemoteEdgeCache) SetRemoteReplication(ctx context.Context, peerClusterID string, entry *RemoteReplicationEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal remote replication: %w", err)
	}
	key := c.keyRemoteReplication(entry.StreamName, peerClusterID)
	if !entry.Available {
		return c.client.Del(ctx, key).Err()
	}
	return c.client.Set(ctx, key, data, remoteReplicationTTL).Err()
}

// GetRemoteReplications returns all peer clusters replicating a given stream.
func (c *RemoteEdgeCache) GetRemoteReplications(ctx context.Context, streamName string) ([]*RemoteReplicationEntry, error) {
	pattern := c.keyRemoteReplicationPattern(streamName)
	return scanEntries[RemoteReplicationEntry](ctx, c.client, pattern)
}

// --- Origin Pull Locking (per-stream, short-lease) ---

// TryAcquireOriginPullLock elects one Foghorn instance to arrange the
// initial origin pull for a stream. The short lease only closes the
// concurrent arrange race; the durable replication mark lives on
// control.StreamRegistry.
func (c *RemoteEdgeCache) TryAcquireOriginPullLock(ctx context.Context, streamName, owner string) bool {
	if streamName == "" || owner == "" {
		return false
	}
	ok, err := c.client.SetNX(ctx, c.keyOriginPullLock(streamName), owner, originPullLockTTL).Result()
	return err == nil && ok
}

// ReleaseOriginPullLock releases the origin-pull lock only if this instance
// still owns it.
func (c *RemoteEdgeCache) ReleaseOriginPullLock(ctx context.Context, streamName, owner string) {
	if streamName == "" || owner == "" {
		return
	}
	releaseLeaseScript.Run(ctx, c.client, []string{c.keyOriginPullLock(streamName)}, owner) //nolint:errcheck
}

// --- Edge Summary (3G: official coverage cluster, TTL 60s) ---

// EdgeSummaryEntry is the per-node snapshot from a ClusterEdgeSummary.
type EdgeSummaryEntry struct {
	NodeID         string   `json:"node_id"`
	BaseURL        string   `json:"base_url"`
	GeoLat         float64  `json:"geo_lat"`
	GeoLon         float64  `json:"geo_lon"`
	BWAvailableAvg uint64   `json:"bw_available_avg"`
	CPUPercentAvg  float64  `json:"cpu_percent_avg"`
	RAMUsed        uint64   `json:"ram_used"`
	RAMMax         uint64   `json:"ram_max"`
	TotalViewers   uint32   `json:"total_viewers"`
	Roles          []string `json:"roles"`
}

// EdgeSummaryRecord is the full cluster summary stored in Redis.
type EdgeSummaryRecord struct {
	Edges     []*EdgeSummaryEntry `json:"edges"`
	Timestamp int64               `json:"timestamp"`
}

// SetEdgeSummary stores a smoothed edge summary from a peer's official coverage cluster.
func (c *RemoteEdgeCache) SetEdgeSummary(ctx context.Context, peerClusterID string, record *EdgeSummaryRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal edge summary: %w", err)
	}
	key := c.keyEdgeSummary(peerClusterID)
	return c.client.Set(ctx, key, data, edgeSummaryTTL).Err()
}

// GetEdgeSummary returns the latest edge summary from a peer cluster, or nil.
func (c *RemoteEdgeCache) GetEdgeSummary(ctx context.Context, peerClusterID string) (*EdgeSummaryRecord, error) {
	key := c.keyEdgeSummary(peerClusterID)
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get edge summary: %w", err)
	}
	var record EdgeSummaryRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal edge summary: %w", err)
	}
	return &record, nil
}

// --- Versioned peer-hint contributions ---

// PeerHint is the shared federation-discovery record: everything a LEADER needs to establish a
// usable, tenant-authorized peer channel for a peer another replica discovered — address alone is
// not enough (a Redis-created peer with no lifecycle/tenants filters every scoped broadcast).
type PeerHint struct {
	Addr     string   `json:"addr"`
	AlwaysOn bool     `json:"always_on,omitempty"`
	Tenants  []string `json:"tenants,omitempty"`
}

func normalizePeerHint(h PeerHint) (PeerHint, error) {
	h.Addr = strings.TrimSpace(h.Addr)
	if h.Addr == "" {
		return PeerHint{}, errors.New("peer hint has empty address")
	}
	seen := make(map[string]struct{}, len(h.Tenants))
	tenants := make([]string, 0, len(h.Tenants))
	for _, tenantID := range h.Tenants {
		tenantID = strings.TrimSpace(tenantID)
		if tenantID == "" {
			continue
		}
		if _, ok := seen[tenantID]; ok {
			continue
		}
		seen[tenantID] = struct{}{}
		tenants = append(tenants, tenantID)
	}
	slices.Sort(tenants)
	h.Tenants = tenants
	if !h.AlwaysOn && len(h.Tenants) == 0 {
		return PeerHint{}, errors.New("stream-scoped peer hint has no tenant scope")
	}
	return h, nil
}

const peerHintContributionVersion = uint32(2)

type peerHintContribution struct {
	Version              uint32              `json:"version"`
	ContributorID        string              `json:"contributor_id"`
	PublishedAtUnixMilli int64               `json:"published_at_unix_milli"`
	Hints                map[string]PeerHint `json:"hints"`
}

// PublishPeerHints replaces one leased contributor's complete authority snapshot. Empty snapshots
// are meaningful: they revoke that contributor immediately. Each contributor has its own key and
// TTL, so refreshing one writer cannot preserve another writer's stale tenants or peers. Readers
// consume only records from this versioned namespace.
func (c *RemoteEdgeCache) PublishPeerHints(ctx context.Context, contributorID string, hints map[string]PeerHint) error {
	contributorID = strings.TrimSpace(contributorID)
	if contributorID == "" {
		return errors.New("peer hint contribution has empty contributor id")
	}
	normalized := make(map[string]PeerHint, len(hints))
	for clusterID, hint := range hints {
		clusterID = strings.TrimSpace(clusterID)
		if clusterID == "" {
			return errors.New("peer hint has empty cluster id")
		}
		var err error
		hint, err = normalizePeerHint(hint)
		if err != nil {
			return fmt.Errorf("normalize peer hint %s: %w", clusterID, err)
		}
		normalized[clusterID] = hint
	}
	record := peerHintContribution{
		Version:              peerHintContributionVersion,
		ContributorID:        contributorID,
		PublishedAtUnixMilli: time.Now().UnixMilli(),
		Hints:                normalized,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal peer hint contribution: %w", err)
	}
	return c.client.Set(ctx, c.keyPeerHintContribution(contributorID), raw, peerAddrTTL).Err()
}

type selectedPeerHint struct {
	hint          PeerHint
	contributorID string
	publishedAt   int64
	unrestricted  bool
}

// GetPeerAddresses aggregates only live v2 contributions. Tenant scope is the union of CURRENT
// leases, while lifecycle/address authority is selected deterministically from the strongest,
// newest contribution. Expired or replaced contributions therefore revoke authority. An invalid
// contribution is omitted independently, leaving other valid contributions usable.
func (c *RemoteEdgeCache) GetPeerAddresses(ctx context.Context) (map[string]PeerHint, error) {
	keys, err := c.scanKeys(ctx, c.keyPeerHintContributionPattern())
	if err != nil {
		return nil, err
	}
	selected := make(map[string]selectedPeerHint)
	for _, key := range keys {
		raw, getErr := c.client.Get(ctx, key).Bytes()
		if errors.Is(getErr, goredis.Nil) {
			continue
		}
		if getErr != nil {
			return nil, getErr
		}
		var record peerHintContribution
		if decodeErr := json.Unmarshal(raw, &record); decodeErr != nil || record.Version != peerHintContributionVersion || strings.TrimSpace(record.ContributorID) == "" {
			c.logger.WithField("key", key).Warn("Ignoring malformed v2 peer-hint contribution")
			continue
		}
		for clusterID, rawHint := range record.Hints {
			clusterID = strings.TrimSpace(clusterID)
			incoming, normalizeErr := normalizePeerHint(rawHint)
			if clusterID == "" || normalizeErr != nil {
				c.logger.WithFields(map[string]interface{}{"key": key, "peer_cluster": clusterID}).Warn("Ignoring invalid peer in v2 hint contribution")
				continue
			}
			current, exists := selected[clusterID]
			if !exists {
				selected[clusterID] = selectedPeerHint{hint: incoming, contributorID: record.ContributorID, publishedAt: record.PublishedAtUnixMilli, unrestricted: incoming.AlwaysOn && len(incoming.Tenants) == 0}
				continue
			}
			incomingUnrestricted := incoming.AlwaysOn && len(incoming.Tenants) == 0
			if current.unrestricted || incomingUnrestricted {
				current.hint.Tenants = nil
				current.unrestricted = true
			} else {
				tenantSet := make(map[string]struct{}, len(current.hint.Tenants)+len(incoming.Tenants))
				for _, tenantID := range current.hint.Tenants {
					tenantSet[tenantID] = struct{}{}
				}
				for _, tenantID := range incoming.Tenants {
					tenantSet[tenantID] = struct{}{}
				}
				current.hint.Tenants = current.hint.Tenants[:0]
				for tenantID := range tenantSet {
					current.hint.Tenants = append(current.hint.Tenants, tenantID)
				}
				slices.Sort(current.hint.Tenants)
			}
			incomingStronger := incoming.AlwaysOn && !current.hint.AlwaysOn
			incomingNewer := incoming.AlwaysOn == current.hint.AlwaysOn && (record.PublishedAtUnixMilli > current.publishedAt ||
				(record.PublishedAtUnixMilli == current.publishedAt && record.ContributorID > current.contributorID))
			if incomingStronger || incomingNewer {
				current.hint.Addr = incoming.Addr
				current.contributorID = record.ContributorID
				current.publishedAt = record.PublishedAtUnixMilli
			}
			current.hint.AlwaysOn = current.hint.AlwaysOn || incoming.AlwaysOn
			selected[clusterID] = current
		}
	}
	hints := make(map[string]PeerHint, len(selected))
	for clusterID, selectedHint := range selected {
		hints[clusterID] = selectedHint.hint
	}
	return hints, nil
}

func (c *RemoteEdgeCache) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := c.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			return keys, nil
		}
	}
}

// --- scan helper ---

// scanEntries scans Redis keys matching a pattern and unmarshals each value.
func scanEntries[T any](ctx context.Context, client goredis.UniversalClient, pattern string) ([]*T, error) {
	var entries []*T
	var cursor uint64
	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", pattern, err)
		}
		if len(keys) > 0 {
			vals, err := client.MGet(ctx, keys...).Result()
			if err != nil {
				return nil, fmt.Errorf("mget: %w", err)
			}
			for _, val := range vals {
				if val == nil {
					continue
				}
				s, ok := val.(string)
				if !ok {
					continue
				}
				var entry T
				if err := json.Unmarshal([]byte(s), &entry); err != nil {
					continue
				}
				entries = append(entries, &entry)
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return entries, nil
}

// --- Remote Live Streams (cross-cluster ingest dedup; 30s live, 1h offline fence) ---

// RemoteLiveStreamEntry records that a stream is live on a peer cluster.
type RemoteLiveStreamEntry struct {
	ClusterID      string `json:"cluster_id"`
	TenantID       string `json:"tenant_id"`
	SourceRevision int64  `json:"source_revision"`
	UpdatedAt      int64  `json:"updated_at"`
}

var applyRemoteStreamLifecycleScript = goredis.NewScript(`
local current = redis.call('get', KEYS[1])
if current then
  local current_revision = string.sub(current, 1, 20)
  local current_state = string.match(current, '^%d+:(%a+):')
  if not current_state then
    return redis.error_reply('malformed remote lifecycle fence')
  end
  if ARGV[1] < current_revision then
    return 0
  end
  if ARGV[1] == current_revision and current_state == 'offline' and ARGV[2] == 'live' then
    return 0
  end
end
redis.call('psetex', KEYS[1], ARGV[4], ARGV[3])
redis.call('sadd', KEYS[2], ARGV[5])
redis.call('pexpire', KEYS[2], ARGV[6])
return 1
`)

// ApplyRemoteStreamLifecycle atomically accepts only current lifecycle revisions. Offline wins an
// equal-revision tie so a delayed live heartbeat cannot resurrect a generation after its close.
func (c *RemoteEdgeCache) ApplyRemoteStreamLifecycle(ctx context.Context, tenantID, internalName string, entry *RemoteLiveStreamEntry, isLive bool) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	internalName = strings.TrimSpace(internalName)
	if entry == nil || tenantID == "" || internalName == "" || strings.TrimSpace(entry.ClusterID) == "" ||
		strings.TrimSpace(entry.TenantID) != tenantID || entry.SourceRevision <= 0 {
		return false, errors.New("remote stream lifecycle requires cluster, tenant, stream, and positive source revision")
	}
	originClusterID := strings.TrimSpace(entry.ClusterID)
	normalized := *entry
	normalized.ClusterID = originClusterID
	normalized.TenantID = tenantID
	data, err := json.Marshal(&normalized)
	if err != nil {
		return false, fmt.Errorf("marshal remote live stream: %w", err)
	}
	state := "offline"
	ttl := remoteOfflineFenceTTL
	if isLive {
		state = "live"
		ttl = remoteLiveStreamTTL
	}
	revision := fmt.Sprintf("%020d", entry.SourceRevision)
	value := revision + ":" + state + ":" + string(data)
	originKey := c.keyRemoteLiveStream(tenantID, internalName, originClusterID)
	originsKey := c.keyRemoteLiveStreamOrigins(tenantID, internalName)
	applied, err := applyRemoteStreamLifecycleScript.Run(ctx, c.client, []string{originKey, originsKey},
		revision, state, value, ttl.Milliseconds(), originClusterID, remoteOfflineFenceTTL.Milliseconds()).Int64()
	if err != nil {
		return false, fmt.Errorf("apply remote stream lifecycle: %w", err)
	}
	return applied == 1, nil
}

// GetRemoteLiveStream returns the peer cluster where a stream is live, or nil.
func (c *RemoteEdgeCache) GetRemoteLiveStream(ctx context.Context, tenantID, internalName string) (*RemoteLiveStreamEntry, error) {
	tenantID = strings.TrimSpace(tenantID)
	internalName = strings.TrimSpace(internalName)
	if tenantID == "" || internalName == "" {
		return nil, nil
	}
	originsKey := c.keyRemoteLiveStreamOrigins(tenantID, internalName)
	originClusterIDs, err := c.client.SMembers(ctx, originsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("get remote live stream origins: %w", err)
	}
	if len(originClusterIDs) == 0 {
		return nil, nil
	}
	slices.Sort(originClusterIDs)
	keys := make([]string, len(originClusterIDs))
	for i, originClusterID := range originClusterIDs {
		keys[i] = c.keyRemoteLiveStream(tenantID, internalName, originClusterID)
	}
	values, err := c.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("get remote live streams: %w", err)
	}
	var live *RemoteLiveStreamEntry
	for i, value := range values {
		if value == nil {
			continue
		}
		encoded, ok := value.(string)
		if !ok {
			return nil, errors.New("unmarshal remote live stream: non-string lifecycle fence")
		}
		entry, isLive, decodeErr := decodeRemoteStreamLifecycle(encoded, tenantID, originClusterIDs[i])
		if decodeErr != nil {
			return nil, decodeErr
		}
		if isLive && live == nil {
			live = entry
		}
	}
	return live, nil
}

func decodeRemoteStreamLifecycle(data, tenantID, originClusterID string) (*RemoteLiveStreamEntry, bool, error) {
	parts := strings.SplitN(data, ":", 3)
	if len(parts) != 3 || (parts[1] != "live" && parts[1] != "offline") {
		return nil, false, errors.New("unmarshal remote live stream: malformed lifecycle fence")
	}
	revision, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || revision <= 0 {
		return nil, false, errors.New("unmarshal remote live stream: malformed source revision")
	}
	var entry RemoteLiveStreamEntry
	if err := json.Unmarshal([]byte(parts[2]), &entry); err != nil {
		return nil, false, fmt.Errorf("unmarshal remote live stream: %w", err)
	}
	if entry.SourceRevision != revision || entry.TenantID != tenantID || entry.ClusterID != originClusterID {
		return nil, false, errors.New("unmarshal remote live stream: lifecycle identity mismatch")
	}
	return &entry, parts[1] == "live", nil
}

// --- Remote Artifact Locations (hot artifacts on peer edges, TTL 90s) ---

const remoteArtifactTTL = 90 * time.Second

// RemoteArtifactEntry records a hot artifact on a specific edge node of a peer cluster.
type RemoteArtifactEntry struct {
	ArtifactHash string  `json:"artifact_hash"`
	ArtifactType string  `json:"artifact_type"`
	PeerCluster  string  `json:"peer_cluster"`
	NodeID       string  `json:"node_id"`
	BaseURL      string  `json:"base_url"`
	SizeBytes    uint64  `json:"size_bytes"`
	AccessCount  uint32  `json:"access_count"`
	LastAccessed int64   `json:"last_accessed"`
	GeoLat       float64 `json:"geo_lat"`
	GeoLon       float64 `json:"geo_lon"`
	UpdatedAt    int64   `json:"updated_at"`
	TenantID     string  `json:"tenant_id,omitempty"`
}

func (c *RemoteEdgeCache) keyRemoteArtifact(peerClusterID, artifactHash, nodeID string) string {
	return fmt.Sprintf("{%s}:remote_artifacts:%s:%s:%s", c.clusterID, peerClusterID, artifactHash, nodeID)
}

func (c *RemoteEdgeCache) keyRemoteArtifactGlob() string {
	return fmt.Sprintf("{%s}:remote_artifacts:*", c.clusterID)
}

// SetRemoteArtifact stores a remote artifact location from a peer.
func (c *RemoteEdgeCache) SetRemoteArtifact(ctx context.Context, peerClusterID string, entry *RemoteArtifactEntry) error {
	entry.PeerCluster = peerClusterID
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal remote artifact: %w", err)
	}
	key := c.keyRemoteArtifact(peerClusterID, entry.ArtifactHash, entry.NodeID)
	return c.client.Set(ctx, key, data, remoteArtifactTTL).Err()
}

// GetRemoteArtifacts returns all peer-cluster locations for a given artifact hash.
// Scans across all peer prefixes: {cluster}:remote_artifacts:*:{hash}
func (c *RemoteEdgeCache) GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*RemoteArtifactEntry, error) {
	pattern := fmt.Sprintf("{%s}:remote_artifacts:*:%s:*", c.clusterID, artifactHash)
	return scanEntries[RemoteArtifactEntry](ctx, c.client, pattern)
}

// GetAllRemoteArtifacts returns all cached remote artifacts across all peers.
func (c *RemoteEdgeCache) GetAllRemoteArtifacts(ctx context.Context) ([]*RemoteArtifactEntry, error) {
	return scanEntries[RemoteArtifactEntry](ctx, c.client, c.keyRemoteArtifactGlob())
}

// --- Peer Heartbeat (per-peer, TTL 30s) ---

// PeerHeartbeatRecord stores the latest heartbeat from a peer cluster.
type PeerHeartbeatRecord struct {
	ProtocolVersion  uint32   `json:"protocol_version"`
	StreamCount      uint32   `json:"stream_count"`
	TotalBWAvailable uint64   `json:"total_bw_available"`
	EdgeCount        uint32   `json:"edge_count"`
	UptimeSeconds    int64    `json:"uptime_seconds"`
	Capabilities     []string `json:"capabilities"`
	Lat              float64  `json:"lat,omitempty"`
	Lon              float64  `json:"lon,omitempty"`
	Location         string   `json:"location,omitempty"`
	ReceivedAt       int64    `json:"received_at"`
}

func (c *RemoteEdgeCache) keyPeerHeartbeat(peerClusterID string) string {
	return fmt.Sprintf("{%s}:peer_heartbeat:%s", c.clusterID, peerClusterID)
}

// SetPeerHeartbeat stores a heartbeat from a peer cluster.
func (c *RemoteEdgeCache) SetPeerHeartbeat(ctx context.Context, peerClusterID string, record *PeerHeartbeatRecord) error {
	record.ReceivedAt = time.Now().Unix()
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal peer heartbeat: %w", err)
	}
	key := c.keyPeerHeartbeat(peerClusterID)
	return c.client.Set(ctx, key, data, peerHeartbeatTTL).Err()
}

// GetPeerHeartbeat returns the latest heartbeat from a peer, or nil.
func (c *RemoteEdgeCache) GetPeerHeartbeat(ctx context.Context, peerClusterID string) (*PeerHeartbeatRecord, error) {
	key := c.keyPeerHeartbeat(peerClusterID)
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get peer heartbeat: %w", err)
	}
	var record PeerHeartbeatRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal peer heartbeat: %w", err)
	}
	return &record, nil
}

// --- Active stream-to-peer membership ---

const streamPeerMembershipVersion = uint32(2)

func (c *RemoteEdgeCache) keyStreamPeerMemberships() string {
	return fmt.Sprintf("{%s}:stream_peer_memberships:v2", c.clusterID)
}

func (c *RemoteEdgeCache) keyStreamPeerMembershipRevisions() string {
	return fmt.Sprintf("{%s}:stream_peer_membership_revisions:v2", c.clusterID)
}

func (c *RemoteEdgeCache) keyStreamPeerMembershipGenerations() string {
	return fmt.Sprintf("{%s}:stream_peer_membership_generations:v2", c.clusterID)
}

func (c *RemoteEdgeCache) keyStreamPeerMembershipStates() string {
	return fmt.Sprintf("{%s}:stream_peer_membership_states:v2", c.clusterID)
}

type StreamPeerTarget struct {
	ClusterID string `json:"cluster_id"`
	Addr      string `json:"addr"`
	AlwaysOn  bool   `json:"always_on,omitempty"`
}

type StreamPeerMembership struct {
	Version          uint32             `json:"version"`
	StreamName       string             `json:"stream_name"`
	TenantID         string             `json:"tenant_id"`
	SourceGeneration string             `json:"source_generation"`
	SourceRevision   int64              `json:"source_revision"`
	Active           bool               `json:"active"`
	EndedAtUnixMilli int64              `json:"ended_at_unix_milli,omitempty"`
	Peers            []StreamPeerTarget `json:"peers"`
}

func normalizeStreamPeerMembership(record StreamPeerMembership) (StreamPeerMembership, error) {
	record.Version = streamPeerMembershipVersion
	record.StreamName = strings.TrimSpace(record.StreamName)
	record.TenantID = strings.TrimSpace(record.TenantID)
	record.SourceGeneration = strings.TrimSpace(record.SourceGeneration)
	if record.StreamName == "" || record.TenantID == "" || record.SourceGeneration == "" || record.SourceRevision <= 0 {
		return StreamPeerMembership{}, errors.New("stream peer membership requires stream, tenant, generation, and positive revision")
	}
	seen := make(map[string]StreamPeerTarget, len(record.Peers))
	peers := make([]StreamPeerTarget, 0, len(record.Peers))
	for _, peer := range record.Peers {
		peer.ClusterID = strings.TrimSpace(peer.ClusterID)
		peer.Addr = strings.TrimSpace(peer.Addr)
		if peer.ClusterID == "" || peer.Addr == "" {
			return StreamPeerMembership{}, errors.New("stream peer membership contains an incomplete peer")
		}
		if existing, ok := seen[peer.ClusterID]; ok {
			if existing.Addr != peer.Addr || existing.AlwaysOn != peer.AlwaysOn {
				return StreamPeerMembership{}, fmt.Errorf("stream peer membership has conflicting peer %s", peer.ClusterID)
			}
			continue
		}
		seen[peer.ClusterID] = peer
		peers = append(peers, peer)
	}
	slices.SortFunc(peers, func(a, b StreamPeerTarget) int { return strings.Compare(a.ClusterID, b.ClusterID) })
	if !record.Active && len(peers) != 0 {
		return StreamPeerMembership{}, errors.New("ended stream peer membership cannot retain peers")
	}
	if record.Active && record.EndedAtUnixMilli != 0 {
		return StreamPeerMembership{}, errors.New("active stream peer membership cannot have an ended timestamp")
	}
	if !record.Active && record.EndedAtUnixMilli <= 0 {
		return StreamPeerMembership{}, errors.New("ended stream peer membership requires an ended timestamp")
	}
	record.Peers = peers
	return record, nil
}

var setStreamPeerMembershipRevisioned = goredis.NewScript(`
local current_revision = redis.call('HGET', KEYS[2], ARGV[1])
if current_revision then
  if ARGV[2] < current_revision then
    return 0
  end
  if ARGV[2] == current_revision then
    if redis.call('HGET', KEYS[3], ARGV[1]) ~= ARGV[3] then
      return -1
    end
    if redis.call('HGET', KEYS[4], ARGV[1]) == 'ended' then
      return 0
    end
    if redis.call('HGET', KEYS[1], ARGV[1]) ~= ARGV[4] then
      return -1
    end
    return 2
  end
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[4])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('HSET', KEYS[3], ARGV[1], ARGV[3])
redis.call('HSET', KEYS[4], ARGV[1], 'active')
return 1
`)

var endStreamPeerMembershipRevisioned = goredis.NewScript(`
local current_revision = redis.call('HGET', KEYS[2], ARGV[1])
if current_revision then
  if ARGV[2] < current_revision then
    return 0
  end
  if ARGV[2] == current_revision then
    if redis.call('HGET', KEYS[3], ARGV[1]) ~= ARGV[3] then
      return -1
    end
    if redis.call('HGET', KEYS[4], ARGV[1]) == 'ended' then
      return 2
    end
  end
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[4])
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('HSET', KEYS[3], ARGV[1], ARGV[3])
redis.call('HSET', KEYS[4], ARGV[1], 'ended')
return 1
`)

func streamPeerRevisionToken(revision int64) string {
	return fmt.Sprintf("%020d", revision)
}

// SetStreamPeerMembership atomically replaces one generation's complete membership, including an
// empty set. A lower revision or an ended equal revision is stale and cannot restore authority.
func (c *RemoteEdgeCache) SetStreamPeerMembership(ctx context.Context, record StreamPeerMembership) (bool, error) {
	record.Active = true
	record.EndedAtUnixMilli = 0
	normalized, err := normalizeStreamPeerMembership(record)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return false, fmt.Errorf("marshal stream peer membership: %w", err)
	}
	result, err := setStreamPeerMembershipRevisioned.Run(ctx, c.client, []string{
		c.keyStreamPeerMemberships(), c.keyStreamPeerMembershipRevisions(),
		c.keyStreamPeerMembershipGenerations(), c.keyStreamPeerMembershipStates(),
	}, normalized.StreamName, streamPeerRevisionToken(normalized.SourceRevision), normalized.SourceGeneration, raw).Int64()
	if err != nil {
		return false, fmt.Errorf("persist stream peer membership: %w", err)
	}
	if result < 0 {
		return false, errors.New("stream peer membership revision has conflicting generation or payload")
	}
	return result > 0, nil
}

// EndStreamPeerMembership retains a revision tombstone. Deleting the field would let a delayed
// TrackStream for the ended generation recreate non-expiring peer authority.
func (c *RemoteEdgeCache) EndStreamPeerMembership(ctx context.Context, record StreamPeerMembership) (bool, error) {
	record.Active = false
	record.Peers = nil
	if record.EndedAtUnixMilli <= 0 {
		record.EndedAtUnixMilli = time.Now().UnixMilli()
	}
	normalized, err := normalizeStreamPeerMembership(record)
	if err != nil {
		return false, err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return false, fmt.Errorf("marshal ended stream peer membership: %w", err)
	}
	result, err := endStreamPeerMembershipRevisioned.Run(ctx, c.client, []string{
		c.keyStreamPeerMemberships(), c.keyStreamPeerMembershipRevisions(),
		c.keyStreamPeerMembershipGenerations(), c.keyStreamPeerMembershipStates(),
	}, normalized.StreamName, streamPeerRevisionToken(normalized.SourceRevision), normalized.SourceGeneration, raw).Int64()
	if err != nil {
		return false, fmt.Errorf("end stream peer membership: %w", err)
	}
	if result < 0 {
		return false, errors.New("ended stream peer membership revision has conflicting generation")
	}
	return result > 0, nil
}

const purgeEndedStreamPeerMembershipLua = `
if redis.call('HGET', KEYS[2], ARGV[1]) ~= ARGV[2] then
  return 0
end
if redis.call('HGET', KEYS[3], ARGV[1]) ~= ARGV[3] then
  return 0
end
if redis.call('HGET', KEYS[4], ARGV[1]) ~= 'ended' then
  return 0
end
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
redis.call('HDEL', KEYS[3], ARGV[1])
redis.call('HDEL', KEYS[4], ARGV[1])
return 1
`

// ScanEndedStreamPeerMemberships advances through the lightweight state hash and loads only ended
// payloads from the scanned page. Callers retain the cursor between bounded cleanup passes.
func (c *RemoteEdgeCache) ScanEndedStreamPeerMemberships(ctx context.Context, cursor uint64, count int64) ([]StreamPeerMembership, uint64, error) {
	if count <= 0 {
		count = 64
	}
	fields, next, err := c.client.HScan(ctx, c.keyStreamPeerMembershipStates(), cursor, "*", count).Result()
	if err != nil {
		return nil, cursor, fmt.Errorf("scan ended stream peer memberships: %w", err)
	}
	endedNames := make([]string, 0, len(fields)/2)
	for index := 0; index+1 < len(fields); index += 2 {
		if fields[index+1] == "ended" {
			endedNames = append(endedNames, fields[index])
		}
	}
	if len(endedNames) == 0 {
		return nil, next, nil
	}
	values, err := c.client.HMGet(ctx, c.keyStreamPeerMemberships(), endedNames...).Result()
	if err != nil {
		return nil, cursor, fmt.Errorf("load ended stream peer memberships: %w", err)
	}
	records := make([]StreamPeerMembership, 0, len(values))
	for index, value := range values {
		if value == nil {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			return nil, cursor, fmt.Errorf("ended stream peer membership %s has non-string payload", endedNames[index])
		}
		var record StreamPeerMembership
		if err = json.Unmarshal([]byte(raw), &record); err != nil {
			return nil, cursor, fmt.Errorf("decode ended stream peer membership %s: %w", endedNames[index], err)
		}
		if record.Version != streamPeerMembershipVersion {
			return nil, cursor, fmt.Errorf("ended stream peer membership %s has version %d", endedNames[index], record.Version)
		}
		record, err = normalizeStreamPeerMembership(record)
		if err != nil {
			return nil, cursor, fmt.Errorf("validate ended stream peer membership %s: %w", endedNames[index], err)
		}
		if !record.Active && record.StreamName == endedNames[index] {
			records = append(records, record)
		}
	}
	return records, next, nil
}

// PurgeEndedStreamPeerMemberships pipelines exact-revision deletions. Each Lua command removes all
// four fields only while its revision/generation is still ended, so a concurrent successor wins.
func (c *RemoteEdgeCache) PurgeEndedStreamPeerMemberships(ctx context.Context, records []StreamPeerMembership) (int, error) {
	pipe := c.client.Pipeline()
	commands := make([]*goredis.Cmd, 0, len(records))
	for _, record := range records {
		if record.Active {
			return 0, errors.New("cannot purge an active stream peer membership")
		}
		normalized, err := normalizeStreamPeerMembership(record)
		if err != nil {
			return 0, err
		}
		commands = append(commands, pipe.Eval(ctx, purgeEndedStreamPeerMembershipLua, []string{
			c.keyStreamPeerMemberships(), c.keyStreamPeerMembershipRevisions(),
			c.keyStreamPeerMembershipGenerations(), c.keyStreamPeerMembershipStates(),
		}, normalized.StreamName, streamPeerRevisionToken(normalized.SourceRevision), normalized.SourceGeneration))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("purge ended stream peer memberships: %w", err)
	}
	purged := 0
	for _, command := range commands {
		result, err := command.Int64()
		if err != nil {
			return purged, fmt.Errorf("read ended stream peer membership purge result: %w", err)
		}
		if result > 0 {
			purged++
		}
	}
	return purged, nil
}

// LoadAllStreamPeerMemberships reads one atomic Redis hash snapshot. Any invalid field rejects the
// complete reconstruction, so a leader never installs partial authority.
func (c *RemoteEdgeCache) LoadAllStreamPeerMemberships(ctx context.Context) (map[string]StreamPeerMembership, error) {
	fields, err := c.client.HGetAll(ctx, c.keyStreamPeerMemberships()).Result()
	if err != nil {
		return nil, fmt.Errorf("read stream peer memberships: %w", err)
	}
	result := make(map[string]StreamPeerMembership, len(fields))
	for field, raw := range fields {
		var record StreamPeerMembership
		if decodeErr := json.Unmarshal([]byte(raw), &record); decodeErr != nil {
			return nil, fmt.Errorf("decode stream peer membership %s: %w", field, decodeErr)
		}
		if record.Version != streamPeerMembershipVersion {
			return nil, fmt.Errorf("stream peer membership %s has version %d", field, record.Version)
		}
		record, err = normalizeStreamPeerMembership(record)
		if err != nil {
			return nil, fmt.Errorf("validate stream peer membership %s: %w", field, err)
		}
		if record.StreamName != field {
			return nil, fmt.Errorf("stream peer membership field %s contains stream %s", field, record.StreamName)
		}
		result[field] = record
	}
	return result, nil
}

// PeerClusterIDFromKey extracts the peer cluster ID from a remote_edges or
// remote_replications key. Returns empty string if the key doesn't match.
func PeerClusterIDFromKey(key string) string {
	parts := strings.Split(key, ":")
	if len(parts) < 4 {
		return ""
	}
	switch parts[1] {
	case "remote_edges":
		return parts[2] // {c}:remote_edges:{peer}:{node}
	case "remote_replications":
		return parts[3] // {c}:remote_replications:{stream}:{peer}
	default:
		return ""
	}
}
