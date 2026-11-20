package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const nodeCountsFreshFor = 30 * time.Second

var ErrNodeStreamCountsSuperseded = errors.New("node audience census superseded")

// NodeStreamCountsSnapshot is a complete audience census from one control connection.
type NodeStreamCountsSnapshot struct {
	NodeID  string            `json:"node_id"`
	Fence   int64             `json:"fence"`
	Seq     int64             `json:"seq"`
	Streams map[string]uint32 `json:"streams"`
}

type nodeStreamCountsState struct {
	snapshot  NodeStreamCountsSnapshot
	appliedAt time.Time
}

func (sm *StreamStateManager) applyNodeStreamCountsLocked(env NodeStreamCountsSnapshot) bool {
	if env.NodeID == "" || env.Fence <= 0 || env.Seq <= 0 {
		return false
	}
	if _, evicted := sm.evictedNodes[env.NodeID]; evicted {
		return false
	}
	incoming := incOrder{fence: env.Fence, seq: env.Seq}
	if previous, ok := sm.nodeStreamCounts[env.NodeID]; ok && !incoming.newerThan(incOrder{fence: previous.snapshot.Fence, seq: previous.snapshot.Seq}) {
		return false
	}
	if sm.nodeStreamCounts == nil {
		sm.nodeStreamCounts = make(map[string]nodeStreamCountsState)
	}
	counts := make(map[string]uint32, len(env.Streams))
	for name, count := range env.Streams {
		counts[name] = count
	}
	env.Streams = counts
	sm.nodeStreamCounts[env.NodeID] = nodeStreamCountsState{snapshot: env, appliedAt: time.Now()}
	for name, nodes := range sm.streamInstances {
		if inst := nodes[env.NodeID]; inst != nil {
			inst.Viewers = int(counts[name])
		}
	}
	return true
}

func (sm *StreamStateManager) audienceViewersLocked(nodeID, streamName string) int {
	node := sm.nodes[nodeID]
	counts, ok := sm.nodeStreamCounts[nodeID]
	if !ok || node == nil || node.IsStale || time.Since(counts.appliedAt) > nodeCountsFreshFor {
		return 0
	}
	return int(counts.snapshot.Streams[streamName])
}

// nodeStreamReportLock serializes census apply with the presence writes it fences.
func (sm *StreamStateManager) nodeStreamReportLock(nodeID string) *sync.Mutex {
	value, _ := sm.nodeStreamReportLocks.LoadOrStore(nodeID, &sync.Mutex{})
	lock, ok := value.(*sync.Mutex)
	if !ok {
		panic("invalid node stream report lock")
	}
	return lock
}

// ApplyNodeStreamReport applies a census and its associated node snapshot in one
// local order. Peer and rehydration census updates use the same per-node lock.
func (sm *StreamStateManager) ApplyNodeStreamReport(ctx context.Context, env NodeStreamCountsSnapshot, apply func()) error {
	lock := sm.nodeStreamReportLock(env.NodeID)
	lock.Lock()
	defer lock.Unlock()
	if env.Fence <= 0 || env.Seq <= 0 {
		sm.mu.RLock()
		_, known := sm.nodeStreamCounts[env.NodeID]
		sm.mu.RUnlock()
		if known {
			return ErrNodeStreamCountsSuperseded
		}
		if apply != nil {
			apply()
			return nil
		}
	}
	err := sm.setNodeStreamCounts(ctx, env)
	if errors.Is(err, ErrNodeStreamCountsSuperseded) {
		return err
	}
	if apply != nil {
		apply()
	}
	return err
}

// SetNodeStreamCounts publishes the fenced census without additional presence writes.
func (sm *StreamStateManager) SetNodeStreamCounts(ctx context.Context, env NodeStreamCountsSnapshot) error {
	return sm.ApplyNodeStreamReport(ctx, env, nil)
}

func (sm *StreamStateManager) setNodeStreamCounts(ctx context.Context, env NodeStreamCountsSnapshot) error {
	if env.NodeID == "" || env.Fence <= 0 || env.Seq <= 0 {
		return errors.New("node audience census requires a positive fence and sequence")
	}
	sm.nodeCountsPublishMu.Lock()
	defer sm.nodeCountsPublishMu.Unlock()
	persistenceLock := sm.nodePersistenceLock(env.NodeID)
	persistenceLock.Lock()
	defer persistenceLock.Unlock()
	sm.mu.RLock()
	store, instanceID := sm.redisStore, sm.instanceID
	previous, known := sm.nodeStreamCounts[env.NodeID]
	_, evicted := sm.evictedNodes[env.NodeID]
	sm.mu.RUnlock()
	if evicted {
		return ErrNodeStreamCountsSuperseded
	}
	if known && !(incOrder{fence: env.Fence, seq: env.Seq}).newerThan(incOrder{fence: previous.snapshot.Fence, seq: previous.snapshot.Seq}) {
		return ErrNodeStreamCountsSuperseded
	}
	var publishErr error
	if store != nil {
		payload, err := json.Marshal(env)
		if err != nil {
			return err
		}
		change, err := json.Marshal(StateChange{InstanceID: instanceID, Entity: StateEntityNodeStreamCounts, Operation: StateOpUpsert, NodeID: env.NodeID, Payload: payload})
		if err != nil {
			return err
		}
		accepted, err := store.setNodeStreamCountsFenced(ctx, env, payload, change)
		if err != nil {
			publishErr = fmt.Errorf("publish node audience census: %w", err)
		} else if !accepted {
			return ErrNodeStreamCountsSuperseded
		}
	}
	sm.mu.Lock()
	sm.applyNodeStreamCountsLocked(env)
	sm.mu.Unlock()
	return publishErr
}

func (r *RedisStateStore) keyNodeStreamCounts(nodeID string) string {
	return fmt.Sprintf("{%s}:node_counts:%s", r.clusterID, nodeID)
}
func (r *RedisStateStore) keyNodeStreamCountsWatermark(nodeID string) string {
	return fmt.Sprintf("{%s}:node_counts_wm:%s", r.clusterID, nodeID)
}

func (r *RedisStateStore) setNodeStreamCountsFenced(ctx context.Context, env NodeStreamCountsSnapshot, payload, change []byte) (bool, error) {
	result, err := setNodeArtifactsFenced.Run(ctx, r.client, []string{r.keyNodeStreamCounts(env.NodeID), r.keyNodeStreamCountsWatermark(env.NodeID), r.changelog.Key()}, payload, env.Fence, env.Seq, change, r.changelog.MaxLen(), fmt.Sprintf("%d-%d", env.Fence, env.Seq)).Int64()
	return result == 1, err
}

func (r *RedisStateStore) getAllNodeStreamCounts() (map[string]*NodeStreamCountsSnapshot, error) {
	return scanRedisMap(r, "{"+r.clusterID+"}:node_counts:*", func(data string) (*NodeStreamCountsSnapshot, string, error) {
		var env NodeStreamCountsSnapshot
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, "", err
		}
		return &env, env.NodeID, nil
	})
}

func (r *RedisStateStore) deleteNodeStreamCounts(nodeID string) error {
	return r.client.Del(context.Background(), r.keyNodeStreamCounts(nodeID), r.keyNodeStreamCountsWatermark(nodeID)).Err()
}

func (sm *StreamStateManager) clearAudienceStreamLocked(nodeID, streamName string) {
	if counts, ok := sm.nodeStreamCounts[nodeID]; ok {
		delete(counts.snapshot.Streams, streamName)
	}
}
