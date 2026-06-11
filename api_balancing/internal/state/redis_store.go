package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

const connOwnerTTL = 60 * time.Second
const pendingDVRStopTTL = 30 * time.Minute

var ErrConnOwnerMissing = errors.New("conn owner key missing")

// deleteConnOwnerIfMatch atomically deletes the key only if its value still
// matches expectedVal, preventing a stale eviction from clobbering a fresh
// owner written by another instance during failover.
var deleteConnOwnerIfMatch = goredis.NewScript(`
if redis.call('get', KEYS[1]) == ARGV[1] then
  return redis.call('del', KEYS[1])
else
  return 0
end
`)

var getAndDelete = goredis.NewScript(`
local val = redis.call('get', KEYS[1])
if val then
  redis.call('del', KEYS[1])
end
return val
`)

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

type StateEntity string

type StateOperation string

const (
	StateEntityNode           StateEntity = "node"
	StateEntityStream         StateEntity = "stream"
	StateEntityStreamInstance StateEntity = "stream_instance"
	StateEntityArtifact       StateEntity = "artifact"
	// StateEntityNodeMode carries a node's operational mode as its own keyed
	// record. The mode is multi-writer (operator API / orchestrator on any
	// instance) and must not ride the whole-node snapshot: an in-flight
	// heartbeat snapshot marshaled before a mode change would republish the
	// old mode at a newer changelog ID, and the node watermark would then
	// block the (older-ID) mode change.
	StateEntityNodeMode StateEntity = "node_mode"

	StateOpUpsert StateOperation = "upsert"
	StateOpDelete StateOperation = "delete"
)

type StateChange struct {
	InstanceID string          `json:"instance_id"`
	Entity     StateEntity     `json:"entity"`
	Operation  StateOperation  `json:"operation"`
	StreamName string          `json:"stream_name,omitempty"`
	NodeID     string          `json:"node_id,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

type streamInstanceRecord struct {
	InternalName string               `json:"internal_name"`
	NodeID       string               `json:"node_id"`
	State        *StreamInstanceState `json:"state"`
}

// nodeModeRecord is the write-through payload for StateEntityNodeMode.
type nodeModeRecord struct {
	NodeID string              `json:"node_id"`
	Mode   NodeOperationalMode `json:"mode"`
	SetBy  string              `json:"set_by,omitempty"`
	SetAt  time.Time           `json:"set_at"`
}

type NodeArtifactState struct {
	NodeID       string `json:"node_id"`
	ClipHash     string `json:"clip_hash"`
	FilePath     string `json:"file_path"`
	SizeBytes    uint64 `json:"size_bytes"`
	StreamName   string `json:"stream_name,omitempty"`
	ArtifactType string `json:"artifact_type,omitempty"`
	Format       string `json:"format,omitempty"`
}

// stateChangelogMaxLen bounds the state changelog stream. At prod write
// rates (a few entries/second/cluster) this retains hours of history —
// far beyond any realistic consumer downtime.
const stateChangelogMaxLen = 100000

type RedisStateStore struct {
	client    goredis.UniversalClient
	changelog *pkgredis.Changelog[StateChange]
	clusterID string
}

func NewRedisStateStore(client goredis.UniversalClient, clusterID string) *RedisStateStore {
	return &RedisStateStore{
		client:    client,
		changelog: pkgredis.NewChangelog[StateChange](client, fmt.Sprintf("{%s}:state_changelog", clusterID), stateChangelogMaxLen),
		clusterID: clusterID,
	}
}

func (r *RedisStateStore) keyNode(nodeID string) string {
	return fmt.Sprintf("{%s}:nodes:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyStream(streamName string) string {
	return fmt.Sprintf("{%s}:streams:%s", r.clusterID, streamName)
}
func (r *RedisStateStore) keyStreamInstance(streamName, nodeID string) string {
	return fmt.Sprintf("{%s}:stream_instances:%s:%s", r.clusterID, streamName, nodeID)
}
func (r *RedisStateStore) keyArtifact(nodeID string) string {
	return fmt.Sprintf("{%s}:artifacts:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyNodeMode(nodeID string) string {
	return fmt.Sprintf("{%s}:node_mode:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyLease(role string) string {
	return fmt.Sprintf("{%s}:lease:%s", r.clusterID, role)
}

func (r *RedisStateStore) setJSON(ctx context.Context, key string, value any) error {
	bytes, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, key, bytes, 0).Err()
}

func (r *RedisStateStore) setJSONRaw(ctx context.Context, key string, payload []byte) error {
	return r.client.Set(ctx, key, payload, 0).Err()
}

func (r *RedisStateStore) TryAcquireLease(ctx context.Context, role, owner string, ttl time.Duration) (bool, error) {
	if owner == "" {
		return false, nil
	}
	key := r.keyLease(role)
	ok, err := r.client.SetNX(ctx, key, owner, ttl).Result()
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	current, err := r.client.Get(ctx, key).Result()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current != owner {
		return false, nil
	}
	return r.RenewLease(ctx, role, owner, ttl)
}

func (r *RedisStateStore) RenewLease(ctx context.Context, role, owner string, ttl time.Duration) (bool, error) {
	if owner == "" {
		return false, nil
	}
	result, err := renewLeaseScript.Run(ctx, r.client, []string{r.keyLease(role)}, owner, int64(ttl/time.Millisecond)).Int64()
	return err == nil && result == 1, err
}

func (r *RedisStateStore) ReleaseLease(ctx context.Context, role, owner string) error {
	if owner == "" {
		return nil
	}
	return releaseLeaseScript.Run(ctx, r.client, []string{r.keyLease(role)}, owner).Err()
}

func (r *RedisStateStore) SetNode(nodeID string, state *NodeState) error {
	return r.setJSON(context.Background(), r.keyNode(nodeID), state)
}

func (r *RedisStateStore) GetAllNodes() (map[string]*NodeState, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:nodes:*", func(data string) (*NodeState, string, error) {
		var n NodeState
		if err := json.Unmarshal([]byte(data), &n); err != nil {
			return nil, "", err
		}
		return &n, n.NodeID, nil
	})
}

func (r *RedisStateStore) DeleteNode(nodeID string) error {
	return r.client.Del(context.Background(), r.keyNode(nodeID)).Err()
}

func (r *RedisStateStore) GetAllNodeModes() (map[string]*nodeModeRecord, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:node_mode:*", func(data string) (*nodeModeRecord, string, error) {
		var rec nodeModeRecord
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, "", err
		}
		return &rec, rec.NodeID, nil
	})
}

func (r *RedisStateStore) DeleteNodeMode(nodeID string) error {
	return r.client.Del(context.Background(), r.keyNodeMode(nodeID)).Err()
}

func (r *RedisStateStore) SetStream(name string, state *StreamState) error {
	return r.setJSON(context.Background(), r.keyStream(name), state)
}

func (r *RedisStateStore) GetAllStreams() (map[string]*StreamState, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:streams:*", func(data string) (*StreamState, string, error) {
		var s StreamState
		if err := json.Unmarshal([]byte(data), &s); err != nil {
			return nil, "", err
		}
		return &s, s.InternalName, nil
	})
}

func (r *RedisStateStore) DeleteStream(name string) error {
	return r.client.Del(context.Background(), r.keyStream(name)).Err()
}

func (r *RedisStateStore) SetStreamInstance(name, nodeID string, state *StreamInstanceState) error {
	rec := streamInstanceRecord{InternalName: name, NodeID: nodeID, State: state}
	return r.setJSON(context.Background(), r.keyStreamInstance(name, nodeID), rec)
}

func (r *RedisStateStore) GetAllStreamInstances() (map[string]map[string]*StreamInstanceState, error) {
	records, err := scanRedisMap(r, "{"+r.clusterID+"}:stream_instances:*", func(data string) (*streamInstanceRecord, string, error) {
		var rec streamInstanceRecord
		if err := json.Unmarshal([]byte(data), &rec); err != nil {
			return nil, "", err
		}
		return &rec, rec.InternalName + ":" + rec.NodeID, nil
	})
	if err != nil {
		return nil, err
	}
	result := make(map[string]map[string]*StreamInstanceState)
	for key, rec := range records {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		if result[parts[0]] == nil {
			result[parts[0]] = make(map[string]*StreamInstanceState)
		}
		result[parts[0]][parts[1]] = rec.State
	}
	return result, nil
}

func (r *RedisStateStore) DeleteStreamInstance(name, nodeID string) error {
	return r.client.Del(context.Background(), r.keyStreamInstance(name, nodeID)).Err()
}

func (r *RedisStateStore) SetNodeArtifacts(nodeID string, artifacts []*NodeArtifactState) error {
	return r.setJSON(context.Background(), r.keyArtifact(nodeID), artifacts)
}

func (r *RedisStateStore) GetAllNodeArtifacts() (map[string][]*NodeArtifactState, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:artifacts:*", func(data string) ([]*NodeArtifactState, string, error) {
		var artifacts []*NodeArtifactState
		if err := json.Unmarshal([]byte(data), &artifacts); err != nil {
			return nil, "", err
		}
		if len(artifacts) == 0 {
			return artifacts, "", nil
		}
		return artifacts, artifacts[0].NodeID, nil
	})
}

// Connection ownership for HA relay: tracks which Foghorn instance holds each node's control stream.
// Value is "instanceID:grpcAddr" so the relay can look up both in a single GET.

func (r *RedisStateStore) keyConnOwner(nodeID string) string {
	return fmt.Sprintf("{%s}:conn_owner:%s", r.clusterID, nodeID)
}

func (r *RedisStateStore) keyPendingDVRStop(internalName string) string {
	return fmt.Sprintf("{%s}:pending_dvr_stop:%s", r.clusterID, internalName)
}

// ConnOwner is the compound value stored in the conn_owner Redis key.
type ConnOwner struct {
	InstanceID string
	GRPCAddr   string
}

func encodeConnOwner(instanceID, grpcAddr string) string {
	return instanceID + "|" + grpcAddr
}

func decodeConnOwner(val string) ConnOwner {
	parts := strings.SplitN(val, "|", 2)
	if len(parts) != 2 {
		return ConnOwner{InstanceID: val}
	}
	return ConnOwner{InstanceID: parts[0], GRPCAddr: parts[1]}
}

func (r *RedisStateStore) SetConnOwner(ctx context.Context, nodeID, instanceID, grpcAddr string) error {
	return r.client.Set(ctx, r.keyConnOwner(nodeID), encodeConnOwner(instanceID, grpcAddr), connOwnerTTL).Err()
}

func (r *RedisStateStore) GetConnOwner(ctx context.Context, nodeID string) (ConnOwner, error) {
	val, err := r.client.Get(ctx, r.keyConnOwner(nodeID)).Result()
	if errors.Is(err, goredis.Nil) {
		return ConnOwner{}, nil
	}
	if err != nil {
		return ConnOwner{}, err
	}
	return decodeConnOwner(val), nil
}

func (r *RedisStateStore) DeleteConnOwner(ctx context.Context, nodeID string) error {
	return r.client.Del(ctx, r.keyConnOwner(nodeID)).Err()
}

// DeleteConnOwnerIfMatch deletes the conn_owner key only if it still holds
// the value for the given instance. Returns true if the key was deleted.
func (r *RedisStateStore) DeleteConnOwnerIfMatch(ctx context.Context, nodeID, instanceID, grpcAddr string) (bool, error) {
	res, err := deleteConnOwnerIfMatch.Run(ctx, r.client,
		[]string{r.keyConnOwner(nodeID)},
		encodeConnOwner(instanceID, grpcAddr),
	).Int64()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}

func (r *RedisStateStore) RefreshConnOwner(ctx context.Context, nodeID string) error {
	ok, err := r.client.Expire(ctx, r.keyConnOwner(nodeID), connOwnerTTL).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrConnOwnerMissing
	}
	return nil
}

func (r *RedisStateStore) RegisterPendingDVRStop(ctx context.Context, internalName string, at time.Time) error {
	if internalName == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	return r.client.Set(ctx, r.keyPendingDVRStop(internalName), at.UTC().Format(time.RFC3339Nano), pendingDVRStopTTL).Err()
}

func (r *RedisStateStore) ConsumePendingDVRStop(ctx context.Context, internalName string) (bool, error) {
	if internalName == "" {
		return false, nil
	}
	val, err := getAndDelete.Run(ctx, r.client, []string{r.keyPendingDVRStop(internalName)}).Result()
	if errors.Is(err, goredis.Nil) || val == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// PublishStateChange appends the change to the cluster's ordered changelog
// and returns the server-assigned entry ID — the change's logical version.
func (r *RedisStateStore) PublishStateChange(change StateChange) (string, error) {
	return r.changelog.Append(context.Background(), change)
}

// ChangelogTail returns the current end of the changelog; reading from it
// yields only changes appended afterwards.
func (r *RedisStateStore) ChangelogTail(ctx context.Context) (string, error) {
	return r.changelog.Tail(ctx)
}

// ReadStateChanges consumes changes after fromID in order until ctx is done.
func (r *RedisStateStore) ReadStateChanges(ctx context.Context, fromID string, handler func(id string, change StateChange)) error {
	return r.changelog.Read(ctx, fromID, handler)
}

type redisScanner[T any] func(data string) (T, string, error)

func scanRedisMap[T any](r *RedisStateStore, pattern string, parser redisScanner[T]) (map[string]T, error) {
	ctx := context.Background()
	cursor := uint64(0)
	result := make(map[string]T)

	for {
		keys, next, err := r.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, err
		}

		for _, key := range keys {
			value, err := r.client.Get(ctx, key).Result()
			if err != nil {
				if stateLogger != nil {
					stateLogger.WithError(err).WithField("key", key).Warn("Failed to GET redis key during scan")
				}
				continue
			}
			parsed, resultKey, err := parser(value)
			if err != nil {
				if stateLogger != nil {
					stateLogger.WithError(err).WithField("key", key).Warn("Failed to parse redis value during scan")
				}
				continue
			}
			if resultKey == "" {
				continue
			}
			result[resultKey] = parsed
		}

		cursor = next
		if cursor == 0 {
			break
		}
	}

	return result, nil
}
