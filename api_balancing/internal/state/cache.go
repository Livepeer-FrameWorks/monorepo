package state

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

// EnableRedisSync wires this instance into the multi-Foghorn cell: changes
// flow through write-through keys (rehydration source) plus an ordered,
// replayable changelog (live sync). The startup sequence is a consistent cut:
// capture the changelog tail FIRST, then load the key snapshot, then replay
// from the captured tail — a change before the capture is fully in the
// snapshot, a change after it is replayed, nothing falls in between.
func (sm *StreamStateManager) EnableRedisSync(ctx context.Context, store *RedisStateStore, instanceID string, logger logging.Logger) error {
	subCtx, cancel := context.WithCancel(ctx)

	sm.mu.Lock()
	sm.redisStore = store
	sm.instanceID = instanceID
	sm.redisCancel = cancel
	sm.mu.Unlock()

	tail, err := store.ChangelogTail(subCtx)
	if err != nil {
		logger.WithError(err).Warn("Failed to read state changelog tail; replaying from start of retained log")
		tail = "0-0"
	}

	if err := sm.rehydrateFromRedis(store); err != nil {
		logger.WithError(err).Warn("Failed to rehydrate in-memory state from redis")
	}

	sm.redisWg.Add(1)
	go func() {
		defer sm.redisWg.Done()
		cursor := tail
		for {
			err := store.ReadStateChanges(subCtx, cursor, sm.handleStateChangelogEntry)
			if errors.Is(err, pkgredis.ErrChangelogGap) && subCtx.Err() == nil {
				// The cursor fell behind the trimmed window (long
				// partition): re-run the consistent cut instead of
				// continuing blind. Re-applying keys is idempotent —
				// snapshots merge and watermarks gate.
				logger.Warn("State changelog reader fell behind retention; re-running consistent cut")
				newTail, tailErr := store.ChangelogTail(subCtx)
				if tailErr != nil {
					newTail = "0-0"
				}
				if rehydrateErr := sm.rehydrateFromRedis(store); rehydrateErr != nil {
					logger.WithError(rehydrateErr).Warn("Failed to re-rehydrate state from redis after changelog gap")
				}
				cursor = newTail
				continue
			}
			if err != nil {
				logger.WithError(err).Warn("Redis state changelog reader stopped")
			}
			return
		}
	}()

	return nil
}

// handleStateChangelogEntry applies one changelog entry: self-originated
// entries only advance the watermark (publish already did, but replay after
// a restart lands here), peer entries apply only when newer than the
// entity's watermark — so a stale or replayed entry can never roll back a
// later local write.
func (sm *StreamStateManager) handleStateChangelogEntry(id string, change StateChange) {
	key := stateChangeKey(change)
	if change.InstanceID == sm.instanceID {
		sm.watermarks.Record(key, id)
		return
	}
	if !sm.watermarks.ShouldApply(key, id) {
		return
	}
	sm.applyRedisChange(change)
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
	// local-only keys are preserved; identity fields merge ignore-empty so a
	// snapshot that never resolved identity can't erase locally-known one).
	for k, v := range nodes {
		if local := sm.nodes[k]; local != nil {
			if v.ClusterID == "" {
				v.ClusterID = local.ClusterID
			}
			if v.TenantID == "" {
				v.TenantID = local.TenantID
			}
		}
		sm.nodes[k] = v
		sm.recomputeNodeScoresLocked(v)
	}
	for k, v := range streams {
		if local := sm.streams[k]; local != nil {
			if v.NodeID == "" {
				v.NodeID = local.NodeID
			}
			if v.TenantID == "" {
				v.TenantID = local.TenantID
			}
			if v.StreamID == "" {
				v.StreamID = local.StreamID
			}
			if v.PlaybackID == "" {
				v.PlaybackID = local.PlaybackID
			}
		}
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
			n.Artifacts = make([]*ipcpb.StoredArtifact, 0, len(arts))
			for _, a := range arts {
				n.Artifacts = append(n.Artifacts, &ipcpb.StoredArtifact{
					ClipHash:     a.ClipHash,
					FilePath:     a.FilePath,
					SizeBytes:    a.SizeBytes,
					StreamName:   a.StreamName,
					ArtifactType: artifactTypeFromString(a.ArtifactType),
					Format:       a.Format,
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
			// Snapshots replace wholesale, but identity merges ignore-empty:
			// a peer that never resolved identity must not erase ours.
			if local := sm.nodes[node.NodeID]; local != nil {
				if node.ClusterID == "" {
					node.ClusterID = local.ClusterID
				}
				if node.TenantID == "" {
					node.TenantID = local.TenantID
				}
			}
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
			if local := sm.streams[stream.InternalName]; local != nil {
				if stream.NodeID == "" {
					stream.NodeID = local.NodeID
				}
				if stream.TenantID == "" {
					stream.TenantID = local.TenantID
				}
				if stream.StreamID == "" {
					stream.StreamID = local.StreamID
				}
				if stream.PlaybackID == "" {
					stream.PlaybackID = local.PlaybackID
				}
			}
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
			if local := sm.streamInstances[change.StreamName][change.NodeID]; local != nil && instance.TenantID == "" {
				instance.TenantID = local.TenantID
			}
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
				n.Artifacts = make([]*ipcpb.StoredArtifact, 0, len(arts))
				for _, a := range arts {
					n.Artifacts = append(n.Artifacts, &ipcpb.StoredArtifact{
						ClipHash:     a.ClipHash,
						FilePath:     a.FilePath,
						SizeBytes:    a.SizeBytes,
						StreamName:   a.StreamName,
						ArtifactType: artifactTypeFromString(a.ArtifactType),
						Format:       a.Format,
					})
				}
			}
		}
	}
}
