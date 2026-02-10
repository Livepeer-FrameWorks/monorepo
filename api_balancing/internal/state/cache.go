package state

import (
	"context"
	"encoding/json"

	pb "frameworks/pkg/proto"

	"frameworks/pkg/logging"
)

func (sm *StreamStateManager) EnableRedisSync(ctx context.Context, store *RedisStateStore, instanceID string, logger logging.Logger) error {
	subCtx, cancel := context.WithCancel(ctx)

	sm.mu.Lock()
	sm.redisStore = store
	sm.instanceID = instanceID
	sm.redisCancel = cancel
	sm.mu.Unlock()

	if err := sm.rehydrateFromRedis(store); err != nil {
		logger.WithError(err).Warn("Failed to rehydrate in-memory state from redis")
	}

	sm.redisWg.Add(1)
	go func() {
		defer sm.redisWg.Done()
		err := store.SubscribeStateChanges(subCtx, func(change StateChange) {
			if change.InstanceID == instanceID {
				return
			}
			sm.applyRedisChange(change)
		})
		if err != nil {
			logger.WithError(err).Warn("Redis state subscription stopped")
		}
	}()

	return nil
}

func (sm *StreamStateManager) rehydrateFromRedis(store *RedisStateStore) error {
	nodes, err := store.GetAllNodes()
	if err != nil {
		return err
	}
	streams, err := store.GetAllStreams()
	if err != nil {
		return err
	}
	instances, err := store.GetAllStreamInstances()
	if err != nil {
		return err
	}
	artifacts, err := store.GetAllNodeArtifacts()
	if err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Merge Redis state into in-memory state (Redis wins for existing keys,
	// local-only keys are preserved).
	for k, v := range nodes {
		sm.nodes[k] = v
		sm.recomputeNodeScoresLocked(v)
	}
	for k, v := range streams {
		sm.streams[k] = v
	}
	for streamName, nodeMap := range instances {
		if sm.streamInstances[streamName] == nil {
			sm.streamInstances[streamName] = make(map[string]*StreamInstanceState)
		}
		for nodeID, inst := range nodeMap {
			sm.streamInstances[streamName][nodeID] = inst
		}
	}
	for nodeID, arts := range artifacts {
		if n := sm.nodes[nodeID]; n != nil {
			n.Artifacts = make([]*pb.StoredArtifact, 0, len(arts))
			for _, a := range arts {
				n.Artifacts = append(n.Artifacts, &pb.StoredArtifact{
					ClipHash:  a.ClipHash,
					FilePath:  a.FilePath,
					SizeBytes: a.SizeBytes,
				})
			}
		}
	}

	return nil
}

func (sm *StreamStateManager) applyRedisChange(change StateChange) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch change.Entity {
	case StateEntityNode:
		if change.Operation == StateOpDelete {
			delete(sm.nodes, change.NodeID)
			return
		}
		var node NodeState
		if err := json.Unmarshal(change.Payload, &node); err == nil {
			sm.nodes[node.NodeID] = &node
			sm.recomputeNodeScoresLocked(&node)
		}
	case StateEntityStream:
		if change.Operation == StateOpDelete {
			delete(sm.streams, change.StreamName)
			delete(sm.streamInstances, change.StreamName)
			return
		}
		var stream StreamState
		if err := json.Unmarshal(change.Payload, &stream); err == nil {
			sm.streams[stream.InternalName] = &stream
		}
	case StateEntityStreamInstance:
		if sm.streamInstances[change.StreamName] == nil {
			sm.streamInstances[change.StreamName] = make(map[string]*StreamInstanceState)
		}
		if change.Operation == StateOpDelete {
			delete(sm.streamInstances[change.StreamName], change.NodeID)
			return
		}
		var instance StreamInstanceState
		if err := json.Unmarshal(change.Payload, &instance); err == nil {
			sm.streamInstances[change.StreamName][change.NodeID] = &instance
		}
	case StateEntityArtifact:
		if change.Operation == StateOpDelete {
			if n := sm.nodes[change.NodeID]; n != nil {
				n.Artifacts = nil
			}
			return
		}
		var arts []*NodeArtifactState
		if err := json.Unmarshal(change.Payload, &arts); err == nil {
			if n := sm.nodes[change.NodeID]; n != nil {
				n.Artifacts = make([]*pb.StoredArtifact, 0, len(arts))
				for _, a := range arts {
					n.Artifacts = append(n.Artifacts, &pb.StoredArtifact{
						ClipHash:  a.ClipHash,
						FilePath:  a.FilePath,
						SizeBytes: a.SizeBytes,
					})
				}
			}
		}
	}
}
