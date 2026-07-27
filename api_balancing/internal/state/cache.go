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

// mergeIncomingNode reconciles a peer-published (or rehydrated) node snapshot
// with local knowledge before it replaces the local entry. Snapshots replace
// wholesale, with three exceptions:
//   - identity (ClusterID/TenantID) merges ignore-empty; a peer that never
//     resolved identity must not erase ours;
//   - OperationalMode is multi-writer with its own changelog entity
//     (StateEntityNodeMode): a snapshot may only fill an empty local mode
//     (mixed-version bootstrap), never overwrite a known one;
//   - AddBandwidth/PendingRedirects are local-only soft state derived from
//     this instance's virtual viewers (its own pending redirects); a peer's
//     penalties are meaningless here and a restart's are dead, so local
//     values are kept and unknown nodes start at zero. The json:"-" scoring
//     inputs (BinHost, EstBandwidthPerUser, LastPollTime) ride along for the
//     same reason; unmarshaling zeroes them, and NodeIDByClientIP depends
//     on BinHost surviving peer snapshot applies.
func mergeIncomingNode(incoming, local *NodeState) {
	if local == nil {
		incoming.AddBandwidth = 0
		incoming.PendingRedirects = 0
		// Artifacts/readiness are owned SOLELY by the fenced artifact envelope (StateEntityArtifact),
		// which orders by (fence, seq). A generic — unfenced — node snapshot must never seed them, or a
		// delayed heartbeat could clobber a newer fenced inventory. A new node starts with no artifacts
		// and cordoned until its fenced envelope arrives.
		incoming.Artifacts = nil
		incoming.ArtifactInventoryReady = false
		return
	}
	if incoming.ClusterID == "" {
		incoming.ClusterID = local.ClusterID
	}
	if incoming.TenantID == "" {
		incoming.TenantID = local.TenantID
	}
	if local.OperationalMode != "" {
		incoming.OperationalMode = local.OperationalMode
	}
	incoming.AddBandwidth = local.AddBandwidth
	incoming.PendingRedirects = local.PendingRedirects
	incoming.BinHost = local.BinHost
	incoming.EstBandwidthPerUser = local.EstBandwidthPerUser
	incoming.LastPollTime = local.LastPollTime
	// PRESERVE the locally-held fenced inventory + readiness: they are owned by the (fence, seq)-ordered
	// artifact envelope, so an unfenced node heartbeat snapshot must not overwrite them with stale data.
	incoming.Artifacts = local.Artifacts
	incoming.ArtifactInventoryReady = local.ArtifactInventoryReady
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
	modes, err := store.GetAllNodeModes()
	if err != nil {
		return err
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Merge Redis state into in-memory state (Redis wins for existing keys,
	// local-only keys are preserved; identity fields merge ignore-empty so a
	// snapshot that never resolved identity can't erase locally-known one).
	for k, v := range nodes {
		mergeIncomingNode(v, sm.nodes[k])
		sm.nodes[k] = v
		sm.recomputeNodeScoresLocked(v)
	}
	// Dedicated mode keys win over whatever (possibly stale) mode the node
	// JSON carried; they are the multi-writer-safe representation. Applied
	// to known nodes only; a mode key without a node key is eviction debris.
	for nodeID, rec := range modes {
		if rec == nil {
			continue
		}
		mode, normErr := normalizeNodeOperationalMode(rec.Mode)
		if normErr != nil {
			continue
		}
		if n := sm.nodes[nodeID]; n != nil {
			n.OperationalMode = mode
		}
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
	for nodeID, env := range artifacts {
		if env == nil {
			continue
		}
		// Restore the accepted ordering watermark from the envelope so a post-restart report is ordered
		// against what Redis already holds — a delayed old-owner report can't win right after rehydration.
		// seq==0 is the takeover marker; its fence is still the ordering floor, so restore it too.
		if env.Fence > 0 && env.Seq >= 0 {
			incoming := incOrder{fence: env.Fence, seq: env.Seq}
			if prev, known := sm.lastAcceptedArtifactOrder[nodeID]; !known || incoming.newerThan(prev) {
				sm.lastAcceptedArtifactOrder[nodeID] = incoming
			}
		}
		if n := sm.nodes[nodeID]; n != nil {
			// Restore the replicated readiness cordon so a restarted instance doesn't permanently exclude
			// an already-confirmed node from artifact routing.
			n.ArtifactInventoryReady = env.Ready
			n.Artifacts = make([]*ipcpb.StoredArtifact, 0, len(env.Artifacts))
			for _, a := range env.Artifacts {
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
			mergeIncomingNode(&node, sm.nodes[node.NodeID])
			sm.nodes[node.NodeID] = &node
			sm.recomputeNodeScoresLocked(&node)
		}
	case StateEntityNodeMode:
		if change.Operation == StateOpDelete {
			return
		}
		var rec nodeModeRecord
		if err := json.Unmarshal(change.Payload, &rec); err != nil {
			return
		}
		nodeID := rec.NodeID
		if nodeID == "" {
			nodeID = change.NodeID
		}
		mode, err := normalizeNodeOperationalMode(rec.Mode)
		if err != nil || nodeID == "" {
			return
		}
		n := sm.nodes[nodeID]
		if n == nil {
			// Mode entry can arrive before the node's first snapshot; the
			// shell fills in on that snapshot and is invisible to the
			// balancer until then (no BaseURL, not healthy).
			n = newNodeState(nodeID)
			sm.nodes[nodeID] = n
		}
		n.OperationalMode = mode
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
		var env NodeArtifactSnapshot
		if err := json.Unmarshal(change.Payload, &env); err == nil {
			// Gate by the report ENVELOPE's (fence, seq), NOT changelog append order: across a
			// conn-ownership handoff a stale old-owner snapshot can be appended AFTER the new owner's and
			// earn a higher entry ID. The envelope carries the ordering identity even for an EMPTY
			// inventory, so an authoritative "node holds nothing" report is ordered too and a stalled old
			// owner's stale empty can't clear a newer inventory. A zero FENCE carries no ordering identity
			// we can trust: REJECT it. seq==0 with a positive fence is the fenced TAKEOVER MARKER (a new
			// owner's Ready=false cordon before its first report) — ordered by fence, accepted below.
			if env.Fence <= 0 || env.Seq < 0 {
				return
			}
			incoming := incOrder{fence: env.Fence, seq: env.Seq}
			if prev, known := sm.lastAcceptedArtifactOrder[change.NodeID]; known && !incoming.newerThan(prev) {
				return
			}
			sm.lastAcceptedArtifactOrder[change.NodeID] = incoming
			if n := sm.nodes[change.NodeID]; n != nil {
				// Replicate readiness + inventory from the envelope. A real report (seq>=1) sets Ready=true
				// and the new inventory; a takeover marker (seq==0) carries Ready=false + empty artifacts, so
				// it re-arms the cordon AND clears the prior owner's inventory — the node serves no stored
				// artifacts on this peer until the new owner's first report. This keeps in-memory, the Redis
				// value, and rehydration consistent (the marker IS the stored envelope until the next report).
				n.ArtifactInventoryReady = env.Ready
				n.Artifacts = make([]*ipcpb.StoredArtifact, 0, len(env.Artifacts))
				for _, a := range env.Artifacts {
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
