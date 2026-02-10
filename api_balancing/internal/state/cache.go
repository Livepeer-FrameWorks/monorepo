package state

import (
	"context"
	"encoding/json"

	"frameworks/pkg/logging"
)

func (sm *StreamStateManager) EnableRedisSync(ctx context.Context, store *RedisStateStore, instanceID string, logger logging.Logger) error {
	sm.mu.Lock()
	sm.redisStore = store
	sm.instanceID = instanceID
	sm.mu.Unlock()

	if err := sm.rehydrateFromRedis(store); err != nil {
		logger.WithError(err).Warn("Failed to rehydrate in-memory state from redis")
	}

	go func() {
		err := store.SubscribeStateChanges(ctx, func(change StateChange) {
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

	sm.mu.Lock()
	sm.nodes = nodes
	sm.streams = streams
	sm.streamInstances = instances
	sm.mu.Unlock()
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
	}
}
