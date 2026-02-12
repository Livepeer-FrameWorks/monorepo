package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	pkgredis "frameworks/pkg/redis"

	goredis "github.com/redis/go-redis/v9"
)

type StateEntity string

type StateOperation string

const (
	StateEntityNode           StateEntity = "node"
	StateEntityStream         StateEntity = "stream"
	StateEntityStreamInstance StateEntity = "stream_instance"
	StateEntityArtifact       StateEntity = "artifact"

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

type NodeArtifactState struct {
	NodeID    string `json:"node_id"`
	ClipHash  string `json:"clip_hash"`
	FilePath  string `json:"file_path"`
	SizeBytes uint64 `json:"size_bytes"`
}

type RedisStateStore struct {
	client    goredis.UniversalClient
	pubsub    *pkgredis.TypedPubSub[StateChange]
	clusterID string
	channel   string
}

func NewRedisStateStore(client goredis.UniversalClient, clusterID string) *RedisStateStore {
	return &RedisStateStore{
		client:    client,
		pubsub:    pkgredis.NewTypedPubSub[StateChange](client),
		clusterID: clusterID,
		channel:   fmt.Sprintf("foghorn:%s:state_updates", clusterID),
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
	return r.client.Set(ctx, r.keyConnOwner(nodeID), encodeConnOwner(instanceID, grpcAddr), 60*time.Second).Err()
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

func (r *RedisStateStore) RefreshConnOwner(ctx context.Context, nodeID string) error {
	return r.client.Expire(ctx, r.keyConnOwner(nodeID), 60*time.Second).Err()
}

func (r *RedisStateStore) PublishStateChange(change StateChange) error {
	return r.pubsub.Publish(context.Background(), r.channel, change)
}

func (r *RedisStateStore) SubscribeStateChanges(ctx context.Context, handler func(StateChange)) error {
	return r.pubsub.Subscribe(ctx, r.channel, handler)
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
