package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/pkg/logging"

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
	remoteEdgeTTL        = 30 * time.Second
	remoteReplicationTTL = 5 * time.Minute
	activeReplicationTTL = 5 * time.Minute
	edgeSummaryTTL       = 60 * time.Second
	leaderLeaseTTL       = 15 * time.Second
	peerAddrTTL          = 30 * time.Second
	remoteLiveStreamTTL  = 30 * time.Second // refreshed every 5s by heartbeat
	streamAdTTL          = 15 * time.Second // refreshed every 5s by pushStreamAds
	playbackIndexTTL     = 30 * time.Second
	peerHeartbeatTTL     = 30 * time.Second // 3 missed 10s heartbeats = dead
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

// Lua scripts for atomic lease operations. Using EVALSHA with EVAL fallback
// avoids the GET-then-mutate TOCTOU race in the previous implementation.
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

func (c *RemoteEdgeCache) keyActiveReplication(streamName string) string {
	return fmt.Sprintf("{%s}:active_replications:%s", c.clusterID, streamName)
}

func (c *RemoteEdgeCache) keyEdgeSummary(peerClusterID string) string {
	return fmt.Sprintf("{%s}:edge_summary:%s", c.clusterID, peerClusterID)
}

func (c *RemoteEdgeCache) keyPeerAddresses() string {
	return fmt.Sprintf("{%s}:peer_addresses", c.clusterID)
}

func (c *RemoteEdgeCache) keyRemoteLiveStream(internalName string) string {
	return fmt.Sprintf("{%s}:remote_live_streams:%s", c.clusterID, internalName)
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

// --- Active Replication Cache (bridge gap, per-stream, TTL 5m) ---

// ActiveReplicationRecord bridges the gap between "origin-pull arranged" and
// "Helmsman reports stream on local edge". Written when NotifyOriginPull
// succeeds; cleared when the stream enters the Local-RIB.
type ActiveReplicationRecord struct {
	StreamName    string    `json:"stream_name"`
	SourceNodeID  string    `json:"source_node_id"`
	SourceCluster string    `json:"source_cluster"`
	DestCluster   string    `json:"dest_cluster"`
	DestNodeID    string    `json:"dest_node_id"`
	DTSCURL       string    `json:"dtsc_url"`
	BaseURL       string    `json:"base_url"`
	CreatedAt     time.Time `json:"created_at"`
}

// SetActiveReplication records an in-flight origin-pull.
func (c *RemoteEdgeCache) SetActiveReplication(ctx context.Context, record *ActiveReplicationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal active replication: %w", err)
	}
	key := c.keyActiveReplication(record.StreamName)
	return c.client.Set(ctx, key, data, activeReplicationTTL).Err()
}

// GetActiveReplication returns the in-flight origin-pull record for a stream, or nil.
func (c *RemoteEdgeCache) GetActiveReplication(ctx context.Context, streamName string) (*ActiveReplicationRecord, error) {
	key := c.keyActiveReplication(streamName)
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active replication: %w", err)
	}
	var record ActiveReplicationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal active replication: %w", err)
	}
	return &record, nil
}

// DeleteActiveReplication removes the in-flight record (stream now in Local-RIB).
func (c *RemoteEdgeCache) DeleteActiveReplication(ctx context.Context, streamName string) error {
	return c.client.Del(ctx, c.keyActiveReplication(streamName)).Err()
}

// GetAllActiveReplications returns all in-flight replication records for this cluster.
func (c *RemoteEdgeCache) GetAllActiveReplications(ctx context.Context) ([]*ActiveReplicationRecord, error) {
	pattern := fmt.Sprintf("{%s}:active_replications:*", c.clusterID)
	var records []*ActiveReplicationRecord
	iter := c.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		data, err := c.client.Get(ctx, iter.Val()).Bytes()
		if err != nil {
			continue
		}
		var record ActiveReplicationRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}
		records = append(records, &record)
	}
	return records, iter.Err()
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

// --- Peer Address Cache (leader writes, all replicas read) ---

// SetPeerAddresses writes the full peer address map to a Redis hash.
// Called by the leader after refreshPeers or demand-driven discovery.
func (c *RemoteEdgeCache) SetPeerAddresses(ctx context.Context, addrs map[string]string) error {
	key := c.keyPeerAddresses()
	pipe := c.client.TxPipeline()
	pipe.Del(ctx, key)
	if len(addrs) > 0 {
		fields := make(map[string]interface{}, len(addrs))
		for clusterID, addr := range addrs {
			fields[clusterID] = addr
		}
		pipe.HSet(ctx, key, fields)
		pipe.Expire(ctx, key, peerAddrTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetPeerAddresses reads the full peer address map from Redis.
// Called by non-leaders to populate their local address cache.
func (c *RemoteEdgeCache) GetPeerAddresses(ctx context.Context) (map[string]string, error) {
	key := c.keyPeerAddresses()
	return c.client.HGetAll(ctx, key).Result()
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

// --- Remote Live Streams (cross-cluster ingest dedup, TTL 30s) ---

// RemoteLiveStreamEntry records that a stream is live on a peer cluster.
type RemoteLiveStreamEntry struct {
	ClusterID string `json:"cluster_id"`
	TenantID  string `json:"tenant_id"`
	UpdatedAt int64  `json:"updated_at"`
}

// SetRemoteLiveStream records that a stream is live on a peer cluster.
func (c *RemoteEdgeCache) SetRemoteLiveStream(ctx context.Context, internalName string, entry *RemoteLiveStreamEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal remote live stream: %w", err)
	}
	key := c.keyRemoteLiveStream(internalName)
	return c.client.Set(ctx, key, data, remoteLiveStreamTTL).Err()
}

// GetRemoteLiveStream returns the peer cluster where a stream is live, or nil.
func (c *RemoteEdgeCache) GetRemoteLiveStream(ctx context.Context, internalName string) (*RemoteLiveStreamEntry, error) {
	key := c.keyRemoteLiveStream(internalName)
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get remote live stream: %w", err)
	}
	var entry RemoteLiveStreamEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal remote live stream: %w", err)
	}
	return &entry, nil
}

// DeleteRemoteLiveStream removes the live-stream record (stream went offline).
func (c *RemoteEdgeCache) DeleteRemoteLiveStream(ctx context.Context, internalName string) error {
	return c.client.Del(ctx, c.keyRemoteLiveStream(internalName)).Err()
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
}

func (c *RemoteEdgeCache) keyRemoteArtifact(peerClusterID, artifactHash string) string {
	return fmt.Sprintf("{%s}:remote_artifacts:%s:%s", c.clusterID, peerClusterID, artifactHash)
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
	key := c.keyRemoteArtifact(peerClusterID, entry.ArtifactHash)
	return c.client.Set(ctx, key, data, remoteArtifactTTL).Err()
}

// GetRemoteArtifacts returns all peer-cluster locations for a given artifact hash.
// Scans across all peer prefixes: {cluster}:remote_artifacts:*:{hash}
func (c *RemoteEdgeCache) GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*RemoteArtifactEntry, error) {
	pattern := fmt.Sprintf("{%s}:remote_artifacts:*:%s", c.clusterID, artifactHash)
	return scanEntries[RemoteArtifactEntry](ctx, c.client, pattern)
}

// GetAllRemoteArtifacts returns all cached remote artifacts across all peers.
func (c *RemoteEdgeCache) GetAllRemoteArtifacts(ctx context.Context) ([]*RemoteArtifactEntry, error) {
	return scanEntries[RemoteArtifactEntry](ctx, c.client, c.keyRemoteArtifactGlob())
}

// --- Stream Advertisement (per-stream, per-peer, TTL 15s) ---

// StreamAdRecord stores a StreamAdvertisement from a peer cluster.
type StreamAdRecord struct {
	InternalName    string          `json:"internal_name"`
	TenantID        string          `json:"tenant_id"`
	PlaybackID      string          `json:"playback_id,omitempty"`
	OriginClusterID string          `json:"origin_cluster_id"`
	IsLive          bool            `json:"is_live"`
	Edges           []*StreamAdEdge `json:"edges"`
	Timestamp       int64           `json:"timestamp"`
	PeerCluster     string          `json:"peer_cluster"`
}

// StreamAdEdge mirrors the StreamEdge proto for Redis storage.
type StreamAdEdge struct {
	NodeID      string  `json:"node_id"`
	BaseURL     string  `json:"base_url"`
	DTSCURL     string  `json:"dtsc_url"`
	IsOrigin    bool    `json:"is_origin"`
	BWAvailable uint64  `json:"bw_available"`
	CPUPercent  float64 `json:"cpu_percent"`
	ViewerCount uint32  `json:"viewer_count"`
	GeoLat      float64 `json:"geo_lat"`
	GeoLon      float64 `json:"geo_lon"`
	BufferState string  `json:"buffer_state"`
}

func (c *RemoteEdgeCache) keyStreamAd(peerClusterID, internalName string) string {
	return fmt.Sprintf("{%s}:stream_ads:%s:%s", c.clusterID, peerClusterID, internalName)
}

func (c *RemoteEdgeCache) keyPlaybackIndex(playbackID string) string {
	return fmt.Sprintf("{%s}:playback_index:%s", c.clusterID, playbackID)
}

// SetStreamAd stores a stream advertisement from a peer.
func (c *RemoteEdgeCache) SetStreamAd(ctx context.Context, peerClusterID string, record *StreamAdRecord) error {
	record.PeerCluster = peerClusterID
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal stream ad: %w", err)
	}
	key := c.keyStreamAd(peerClusterID, record.InternalName)
	if !record.IsLive {
		return c.client.Del(ctx, key).Err()
	}
	return c.client.Set(ctx, key, data, streamAdTTL).Err()
}

// GetStreamAd returns the stream advertisement from a specific peer for a stream.
func (c *RemoteEdgeCache) GetStreamAd(ctx context.Context, peerClusterID, internalName string) (*StreamAdRecord, error) {
	key := c.keyStreamAd(peerClusterID, internalName)
	data, err := c.client.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get stream ad: %w", err)
	}
	var record StreamAdRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("unmarshal stream ad: %w", err)
	}
	return &record, nil
}

// GetStreamAdsByName returns all peer advertisements for a given stream (across all peers).
func (c *RemoteEdgeCache) GetStreamAdsByName(ctx context.Context, internalName string) ([]*StreamAdRecord, error) {
	pattern := fmt.Sprintf("{%s}:stream_ads:*:%s", c.clusterID, internalName)
	return scanEntries[StreamAdRecord](ctx, c.client, pattern)
}

// SetPlaybackIndex stores a playback_id → internal_name reverse mapping.
func (c *RemoteEdgeCache) SetPlaybackIndex(ctx context.Context, playbackID, internalName string) error {
	key := c.keyPlaybackIndex(playbackID)
	return c.client.Set(ctx, key, internalName, playbackIndexTTL).Err()
}

// GetPlaybackIndex resolves a playback_id to an internal_name from peer advertisements.
func (c *RemoteEdgeCache) GetPlaybackIndex(ctx context.Context, playbackID string) (string, error) {
	key := c.keyPlaybackIndex(playbackID)
	val, err := c.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return "", nil
	}
	return val, err
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
