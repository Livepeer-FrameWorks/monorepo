package control

import (
	"context"
	"errors"
	"hash/fnv"
	"sort"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// ManagedStreamOwnerTag tags every Mist stream Foghorn provisions through
// the managed-stream channel. Sidecar uses it to scope deletes so it never
// touches tenant push/pull streams.
const ManagedStreamOwnerTag = "fw:managed:foghorn"

// ManagedStreamMaterializer is the integration surface for the per-stream
// side-effects PUSH_REWRITE owns today (stream-context cache writes and
// auto-DVR start) but mist_native streams never trigger because they bypass
// PUSH_REWRITE. The triggers package implements this and registers itself
// via SetManagedStreamMaterializer at startup. nil is a valid value — when
// unset, the reconciler still emits Apply/Retract correctly but downstream
// thumbnails / DVR rely on whatever path is wired separately.
type ManagedStreamMaterializer interface {
	// PopulateStreamContext writes the same caches handlePushRewrite writes
	// (tenantId:internalName + process:internalName) so STREAM_PROCESS finds
	// the per-stream process policy. Called each reconciler tick that emits
	// or refreshes an Apply, so cache TTL stays warm for always_on streams.
	PopulateStreamContext(streamCtx *pb.ResolveStreamContextResponse)

	// EnsureManagedStreamDVR idempotently starts DVR for (stream_id, source_node_id)
	// when is_recording_enabled is true. Must be a no-op if a live DVR for
	// (stream_id, source_node_id) already exists; placement-change cleanup
	// happens via the natural STREAM_END → StopDVRByInternalName path.
	EnsureManagedStreamDVR(ctx context.Context, streamCtx *pb.ResolveStreamContextResponse, sourceNodeID string)
}

var managedStreamMaterializer ManagedStreamMaterializer

// SetManagedStreamMaterializer registers the materializer the reconciler
// uses for per-stream cache + DVR side-effects. Triggers package wires this
// at startup; tests pass a stub.
func SetManagedStreamMaterializer(m ManagedStreamMaterializer) {
	managedStreamMaterializer = m
}

// managedStreamSnapshot is the identity of a managed-stream Apply on a given
// node. The diff-on-Apply gate compares applyKey() across ticks; internalName
// is captured at Apply time so Retract has the Mist stream name even when the
// commodore.streams row has been deleted by the time the diff fires.
type managedStreamSnapshot struct {
	sourceSpec   string
	alwaysOn     bool
	ingestMode   string
	internalName string
}

// applyKey returns the subset of fields that defines whether Foghorn must
// emit a fresh ApplyManagedStream. internalName is intentionally excluded:
// it is stable per stream_id and only carried for Retract.
func (s managedStreamSnapshot) applyKey() managedStreamSnapshot {
	s.internalName = ""
	return s
}

// ForgetManagedStreamLastSent drops cached Apply state for a node across
// every cluster Foghorn serves. Called when a Helmsman disconnects so a
// reconnect's next reconciler tick re-emits Apply for whatever should be
// on that node.
func ForgetManagedStreamLastSent(nodeID string) {
	if StreamRegistryInstance == nil {
		return
	}
	StreamRegistryInstance.ManagedForgetNode(nodeID)
}

// UpdateVerifiedAppliedFromHeartbeat replaces this node's verified-applied
// set with the snapshot the sidecar just reported. Called from the
// Heartbeat handler; nodeID is the canonical ID.
//
// Stores the full apply key (source / always_on / ingest_mode) — not just
// presence — so the reconciler can detect Mist-add failures on UPDATE: if
// the new source failed to land, the sidecar's appliedManagedStreams
// still reports the previous config and presence-only verification would
// mask the drift. The reconciler treats a stream as verified only when
// the heartbeat snapshot matches the desired apply key for that tick.
func UpdateVerifiedAppliedFromHeartbeat(nodeID string, applied []*pb.AppliedManagedStream) {
	if StreamRegistryInstance == nil {
		return
	}
	StreamRegistryInstance.ManagedSetVerifiedFromHeartbeat(nodeID, applied)
}

// managedStreamVerifiedAppliedMatches reports whether the sidecar's
// heartbeat snapshot for (nodeID, streamID) has the same apply key as
// the reconciler's desired snapshot. Returns false when missing OR when
// any field on the apply key differs — which is the correct signal to
// re-emit Apply, because a UPDATE that failed Mist-side still leaves the
// previous-config snapshot in the sidecar's applied map.
func managedStreamVerifiedAppliedMatches(nodeID, streamID string, desired managedStreamSnapshot) bool {
	if StreamRegistryInstance == nil {
		return false
	}
	return StreamRegistryInstance.ManagedVerifiedMatches(nodeID, streamID, desired)
}

// managedStreamVerifiedAppliedPresent reports presence-only (used for
// retract: any record of the stream means Mist still has the config,
// regardless of which version).
func managedStreamVerifiedAppliedPresent(nodeID, streamID string) bool {
	if StreamRegistryInstance == nil {
		return false
	}
	return StreamRegistryInstance.ManagedVerifiedPresent(nodeID, streamID)
}

// HydrateManagedStreamLastSentForNode seeds lastSent with the sidecar's
// reported applied set on (re)connect. Called from the Register handler so a
// Foghorn restart does not lose track of streams the previous Foghorn process
// already provisioned: the next reconciler tick computes desired-vs-applied
// against this seeded state and emits Retract for any stream that has fallen
// out of the desired set during the restart window.
//
// Cluster scope is unknown at Register time (lookup against Quartermaster
// happens later), so the entry is recorded against a sentinel
// pending-cluster key that the next reconciler tick reads and migrates into
// the right cluster bucket once the cluster is known.
//
// Each entry MUST carry stream_id — the lastSent map key has to match the
// reconciler's per-tick key (also stream_id) or hydration would cause a
// double-record race: the reconciler would Apply under stream_id then
// Retract the bare-name entry as "no longer admitted", taking the live
// stream down. Sidecar embeds stream_id in the owner tags on Apply so
// Mist-config hydration recovers it across sidecar restarts too.
func HydrateManagedStreamLastSentForNode(nodeID string, applied []*pb.AppliedManagedStream) {
	if StreamRegistryInstance == nil {
		return
	}
	StreamRegistryInstance.ManagedHydrateForNode(nodeID, applied)
}

// managedStreamPendingClusterKey holds hydrated-but-unclustered lastSent
// entries from Register-time hydration. The reconciler migrates these into
// the right cluster bucket on its first tick after Helmsman registers.
const managedStreamPendingClusterKey = "__pending__"

// adoptHydratedManagedStreams moves any pending hydrated entries for nodes
// in the given cluster into clusterID's bucket. Called from the reconciler
// at tick start so the elected-vs-applied diff for cluster X sees the
// streams Helmsmen in cluster X reported on connect.
func adoptHydratedManagedStreams(clusterID string, clusterNodes []string) {
	if StreamRegistryInstance == nil {
		return
	}
	StreamRegistryInstance.ManagedAdoptPending(clusterID, clusterNodes)
}

// StartManagedStreamReconciler runs Foghorn's managed-stream reconciler at
// the given interval. Each tick: for every cluster Foghorn serves, fetch the
// desired-state set from Commodore, run deterministic placement, and emit
// Apply / Retract deltas vs the last-Applied state per node.
func StartManagedStreamReconciler(ctx context.Context, interval time.Duration, log logging.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	runManagedStreamReconcileOnce(ctx, log)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runManagedStreamReconcileOnce(ctx, log)
		}
	}
}

func runManagedStreamReconcileOnce(ctx context.Context, log logging.Logger) {
	if CommodoreClient == nil {
		return
	}
	for _, clusterID := range ServedClustersSnapshot() {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := CommodoreClient.ListManagedStreams(cctx, clusterID)
		cancel()
		if err != nil {
			log.WithError(err).WithField("cluster_id", clusterID).Warn("ListManagedStreams failed")
			continue
		}
		reconcileClusterManagedStreams(ctx, log, clusterID, resp.GetStreams())
	}
}

// reconcileClusterManagedStreams is the per-cluster body of one reconciler
// tick. Per stream-row, per elected node:
//
//  1. Resolve admission via Commodore. The outcome falls into three buckets:
//     OK (proceed), denied (explicit !admitted — tenant suspended, negative
//     balance, etc.), or transient error (RPC failure). Denied streams must
//     be retracted; transient errors must NOT retract so a Commodore blip
//     does not knock an always-on stream offline.
//  2. Materialize per-stream side-effects (stream-context cache writes,
//     auto-DVR if recording_enabled) on every admitted tick. The reconciler
//     interval (30s) is shorter than the cache TTL (1m prepaid / 10m
//     postpaid), so the periodic re-write refreshes the entry before it
//     expires and keeps STREAM_PROCESS lookups warm for always_on streams.
//  3. Send ApplyManagedStream only on snapshot change (or first send).
//     Sidecar is idempotent, but skipping unchanged-snapshot sends avoids
//     hot-looping the control channel.
//  4. Record lastSent only after a successful send, with internal_name
//     captured so Retract reaches the right Mist stream even when the
//     commodore.streams row has been deleted by then.
//  5. Retract is driven by the admitted-this-tick set, NOT the elected set.
//     A denied stream stays in elected (placement still picked it) but is
//     absent from admitted, so the retract loop fires. Transient-error
//     streams are tracked separately so they neither Apply nor Retract.
func reconcileClusterManagedStreams(ctx context.Context, log logging.Logger, clusterID string, rows []*pb.ManagedStreamRow) {
	localNodes := connectedNodesInCluster(clusterID)
	sort.Strings(localNodes)
	// Migrate any Register-hydrated entries for these nodes into this
	// cluster's bucket before the diff. Without this, a freshly-reconnected
	// Helmsman's reported applied set would sit in the pending bucket and
	// never participate in the retract decision for its actual cluster.
	adoptHydratedManagedStreams(clusterID, localNodes)

	rowByID := make(map[string]*pb.ManagedStreamRow, len(rows))
	type electedEntry struct {
		snap             managedStreamSnapshot
		electedClusterID string
	}
	elected := make(map[string]map[string]electedEntry)
	placementTransientStreams := make(map[string]struct{})
	// transientNodes[nodeID] is the set of stream_ids whose node-specific
	// authority check or admission lookup failed transiently, or whose
	// command stream is owned by a peer Foghorn. Neither Apply nor Retract:
	// leave the peer-owned/prior state in place and re-check next tick.
	transientNodes := make(map[string]map[string]struct{})

	for _, row := range rows {
		rowByID[row.GetStreamId()] = row
		// Mist-native placement is scoped to one source cluster today. The
		// field remains an array for pull-stream symmetry, but bootstrap + DB
		// validation reject multiple source clusters for mist_native because
		// there is no cross-cluster source election authority.
		eligible, placementStatus := eligibleNodesAcrossClustersStatus(row.GetAllowedClusterIds())
		if placementStatus == placementTransient {
			placementTransientStreams[row.GetStreamId()] = struct{}{}
			continue
		}
		if len(eligible) == 0 {
			continue
		}
		count := int(row.GetPlacementCount())
		if count <= 0 {
			count = 1
		}
		if count > len(eligible) {
			count = len(eligible)
		}
		snap := managedStreamSnapshot{
			sourceSpec: row.GetSourceSpec(),
			alwaysOn:   row.GetAlwaysOn(),
			ingestMode: row.GetIngestMode(),
		}
		for _, n := range placementPickWithCluster(row.GetStreamId(), eligible, count) {
			// Cluster-ownership filter: skip when the elected node is in a
			// peer reconciler's cluster. The peer's tick will handle it.
			if n.clusterID != clusterID {
				continue
			}
			// Connection-ownership filter: within the same cluster, peer
			// Foghorn instances may each see the same election but only
			// one holds the Helmsman's bidi stream. Without this filter,
			// the non-owner peer would relay Apply via commandRelay every
			// tick — sidecar idempotency makes that non-destructive but
			// it floods the relay channel and (critically) the non-owner
			// never receives the node's Heartbeats, so verifiedApplied
			// stays empty and snapshotStable && !verifiedApplied keeps
			// firing. Only the connection-owner reconciles the elected
			// node; peers skip.
			owns, ownerStatus := managedStreamOwnsConnectionStatus(n.nodeID)
			if ownerStatus == placementTransient {
				if transientNodes[n.nodeID] == nil {
					transientNodes[n.nodeID] = make(map[string]struct{})
				}
				transientNodes[n.nodeID][row.GetStreamId()] = struct{}{}
				continue
			}
			if !owns {
				if transientNodes[n.nodeID] == nil {
					transientNodes[n.nodeID] = make(map[string]struct{})
				}
				transientNodes[n.nodeID][row.GetStreamId()] = struct{}{}
				continue
			}
			if elected[n.nodeID] == nil {
				elected[n.nodeID] = make(map[string]electedEntry)
			}
			elected[n.nodeID][row.GetStreamId()] = electedEntry{snap: snap, electedClusterID: n.clusterID}
		}
	}

	// admittedNodes[nodeID] is the set of stream_ids that admitted this tick
	// on that node. retract_loop diffs against this, NOT against elected, so
	// denied-this-tick streams (in elected but not admitted) get retracted.
	admittedNodes := make(map[string]map[string]struct{})
	// Snapshot the current per-cluster lastSent under a brief lock so the
	// per-stream loop below (which performs ResolveStreamContext, sidecar
	// sends, materializer hooks, RecordStreamActiveCluster, retract sends)
	// runs WITHOUT holding the global mutex. The cluster reconciler runs
	// per-cluster sequentially, so the snapshot stays consistent within
	// a tick; concurrent ForgetManagedStreamLastSent / hydration mutate
	// specific (cluster, node) entries which the per-stream write-back
	// pattern below handles correctly.
	nodeLastSnap := StreamRegistryInstance.ManagedSnapshotCluster(clusterID)
	if nodeLastSnap == nil {
		nodeLastSnap = make(map[string]map[string]managedStreamSnapshot)
	}

	for nodeID, streamSet := range elected {
		nodeLast := nodeLastSnap[nodeID]
		for sid, entry := range streamSet {
			snap := entry.snap
			electedClusterID := entry.electedClusterID
			row := rowByID[sid]
			// Use the elected node's actual cluster for admission +
			// active-cluster pinning, NOT the reconciler's loop variable.
			// In this iteration the ownership filter guarantees they
			// match, but the explicit pass-through prevents future
			// regressions when ownership semantics relax.
			streamCtx, status := materializeManagedStream(ctx, log, electedClusterID, nodeID, row)
			switch status {
			case materializeTransient:
				if transientNodes[nodeID] == nil {
					transientNodes[nodeID] = make(map[string]struct{})
				}
				transientNodes[nodeID][sid] = struct{}{}
				continue
			case materializeDenied:
				// Intentionally not added to admittedNodes — falls through to
				// the retract loop which will emit Retract iff lastSent has
				// the (node, stream) recorded from a previous tick.
				continue
			case materializeOK:
				// proceed to Apply gate below
			}

			if admittedNodes[nodeID] == nil {
				admittedNodes[nodeID] = make(map[string]struct{})
			}
			admittedNodes[nodeID][sid] = struct{}{}

			snapshotStable := false
			if prev, present := nodeLast[sid]; present && prev.applyKey() == snap.applyKey() {
				snapshotStable = true
			}
			// Verified-applied check: sidecar's Heartbeat-borne applied
			// snapshot is the ground truth. Match against the desired
			// apply key — not just stream-id presence — so a Mist-add
			// failure on UPDATE (sidecar's old snapshot stays in its
			// applied map) is detected as drift and triggers re-Apply.
			verifiedApplied := managedStreamVerifiedAppliedMatches(nodeID, sid, snap)
			needsReapply := snapshotStable && !verifiedApplied

			if !snapshotStable || needsReapply {
				if err := sendApplyManagedStream(log, electedClusterID, nodeID, row, streamCtx); err != nil {
					log.WithError(err).WithFields(logging.Fields{
						"cluster_id": electedClusterID,
						"node_id":    nodeID,
						"stream_id":  sid,
					}).Warn("Apply managed stream failed")
					// Do not record active cluster — routing pin would point
					// playback at a cluster where the Mist config never landed.
					continue
				}
				snap.internalName = streamCtx.GetInternalName()
				StreamRegistryInstance.ManagedSetLastSent(clusterID, nodeID, sid, snap)
				// Keep the local snapshot consistent for the retract pass
				// below (same tick).
				if nodeLast == nil {
					nodeLast = make(map[string]managedStreamSnapshot)
					nodeLastSnap[nodeID] = nodeLast
				}
				nodeLast[sid] = snap
			}

			// Record the elected cluster only after the sidecar has
			// confirmed the stream is in Mist's applied set (via its
			// Heartbeat snapshot). Without this gate, an Apply that the
			// wire-send succeeded for but Mist rejected would silently
			// pin routing at a cluster where playback would 404. Uses
			// the elected node's cluster, not the reconciler's loop var.
			if verifiedApplied {
				recordActiveClusterForManagedStream(log, electedClusterID, sid)
			}
		}
	}

	// Retract pass: iterate the local snapshot WITHOUT holding the lock;
	// network sends + verified-applied checks happen lock-free. Deletions
	// to the live state go through brief targeted locks after each successful
	// retract.
	for nodeID, prev := range nodeLastSnap {
		nodeAdmitted := admittedNodes[nodeID]
		nodeTransient := transientNodes[nodeID]
		for sid, prevSnap := range prev {
			if _, ok := placementTransientStreams[sid]; ok {
				continue
			}
			if !shouldRetractManagedStream(sid, nodeAdmitted, nodeTransient) {
				continue
			}
			if err := sendRetractManagedStream(log, nodeID, sid, prevSnap.internalName); err != nil {
				log.WithError(err).WithFields(logging.Fields{
					"cluster_id": clusterID,
					"node_id":    nodeID,
					"stream_id":  sid,
				}).Warn("Retract managed stream failed")
				continue
			}
			// Drop from live lastSent only after sidecar confirms via
			// Heartbeat that Mist no longer has the stream. Keeping the
			// entry until verified-absent makes the retract loop re-send
			// on the next tick if mistClient.DeleteStream failed. This
			// is presence-only: any record of the stream means Mist
			// still has the config, regardless of which version.
			if managedStreamVerifiedAppliedPresent(nodeID, sid) {
				continue
			}
			// Verified retract: clear active_ingest_cluster_id so public
			// content routing stops pointing at the now-empty cluster.
			// Conditional on the recorded value matching this cluster,
			// so a concurrent peer-cluster placement isn't clobbered.
			clearActiveClusterForManagedStream(log, clusterID, sid)
			StreamRegistryInstance.ManagedDeleteLastSent(clusterID, nodeID, sid)
		}
	}
}

// clearActiveClusterForManagedStream nulls commodore.streams.active_
// ingest_cluster_id once a managed stream has been verified-retracted
// from Mist. The conditional UPDATE (matches on expected_cluster_id)
// makes this safe to call from any reconciler tick — concurrent peer
// activity in another cluster won't have its pin wiped.
func clearActiveClusterForManagedStream(log logging.Logger, clusterID, streamID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := CommodoreClient.ClearStreamActiveCluster(ctx, streamID, clusterID); err != nil {
		log.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Warn("ClearStreamActiveCluster failed after verified retract; routing column may lag")
	}
}

// materializeStatus distinguishes the three reconcile outcomes for a
// (stream, cluster, node) materialize attempt: OK (Apply + record), denied
// (Retract any prior Apply), or transient (preserve prior state, retry).
type materializeStatus int

const (
	materializeOK materializeStatus = iota
	materializeDenied
	materializeTransient
)

// materializeManagedStream resolves admission for a (stream, cluster) pair
// and replays the PUSH_REWRITE-equivalent side-effects (stream-context cache
// + auto-DVR) when admitted. Returns the resolved context plus the status
// the caller acts on. ResolveStreamContext is cheap and already cached
// server-side, so running it every tick is fine even when snapshots are
// stable.
func materializeManagedStream(ctx context.Context, log logging.Logger, clusterID, nodeID string, row *pb.ManagedStreamRow) (*pb.ResolveStreamContextResponse, materializeStatus) {
	if row == nil {
		return nil, materializeTransient
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	streamCtx, err := CommodoreClient.ResolveStreamContext(rctx, row.GetStreamId(), "", "", clusterID)
	cancel()
	if err != nil {
		log.WithError(err).WithFields(logging.Fields{
			"cluster_id": clusterID,
			"node_id":    nodeID,
			"stream_id":  row.GetStreamId(),
		}).Warn("ResolveStreamContext failed; preserving prior state, will retry next tick")
		return nil, materializeTransient
	}
	if !streamCtx.GetAdmitted() {
		log.WithFields(logging.Fields{
			"cluster_id":       clusterID,
			"stream_id":        row.GetStreamId(),
			"playback_id":      row.GetPlaybackId(),
			"admission_reason": streamCtx.GetAdmissionReason(),
		}).Info("Managed stream not admitted; retracting if previously applied")
		return streamCtx, materializeDenied
	}
	if managedStreamMaterializer != nil {
		managedStreamMaterializer.PopulateStreamContext(streamCtx)
		if streamCtx.GetIsRecordingEnabled() {
			managedStreamMaterializer.EnsureManagedStreamDVR(ctx, streamCtx, nodeID)
		}
	}
	return streamCtx, materializeOK
}

// shouldRetractManagedStream returns true when a (cluster, node, stream)
// pair previously recorded in lastSent must be retracted this tick.
// Admitted-this-tick → keep. Transient-error this tick → keep (preserve
// prior state, retry next tick). Anything else (explicit denial OR no
// longer elected by placement) → retract.
func shouldRetractManagedStream(streamID string, admitted, transient map[string]struct{}) bool {
	if _, ok := admitted[streamID]; ok {
		return false
	}
	if _, ok := transient[streamID]; ok {
		return false
	}
	return true
}

func sendApplyManagedStream(log logging.Logger, clusterID, nodeID string, row *pb.ManagedStreamRow, streamCtx *pb.ResolveStreamContextResponse) error {
	if streamCtx.GetInternalName() == "" {
		return errors.New("ResolveStreamContext returned empty internal_name")
	}
	req := &pb.ApplyManagedStream{
		Name:         streamCtx.GetInternalName(),
		Source:       row.GetSourceSpec(),
		AlwaysOn:     row.GetAlwaysOn(),
		Realtime:     false,
		StopSessions: false,
		Tags:         []string{ManagedStreamOwnerTag, "ingest:" + row.GetIngestMode()},
		IngestMode:   row.GetIngestMode(),
		StreamId:     row.GetStreamId(),
		TenantId:     streamCtx.GetTenantId(),
	}
	_ = log
	_ = clusterID
	return SendApplyManagedStream(nodeID, req)
}

// recordActiveClusterForManagedStream pins the elected cluster in
// commodore.streams.active_ingest_cluster_id. Called only after the
// sidecar's Heartbeat-borne applied snapshot confirms the stream is in
// Mist's config (the verifiedApplied gate in the reconciler); without
// that gate, a wire-send-succeeds-but-Mist-rejects situation would pin
// routing at a cluster where playback would 404. Idempotent on retry —
// the same UPDATE runs every subsequent tick that re-verifies, so a
// transient RPC error converges on the next tick.
func recordActiveClusterForManagedStream(log logging.Logger, clusterID, streamID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := CommodoreClient.RecordStreamActiveCluster(ctx, streamID, clusterID)
	if err != nil {
		log.WithError(err).WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Warn("RecordStreamActiveCluster failed; will retry next tick")
		return
	}
	if !resp.GetUpdated() {
		// Another cluster holds a fresh lease (active_ingest_cluster_updated_at
		// within the 30s contended-update window). Log once per tick so
		// operators see the placement conflict; the next reconciler tick
		// will try again as leases age out.
		log.WithFields(logging.Fields{
			"stream_id":  streamID,
			"cluster_id": clusterID,
		}).Info("RecordStreamActiveCluster noop: fresher claim held by another cluster")
	}
}

func sendRetractManagedStream(log logging.Logger, nodeID, streamID, internalName string) error {
	if internalName == "" {
		// Should not happen — lastSent only records after a successful Apply
		// which captures the internalName. Belt-and-suspenders: log + drop.
		log.WithFields(logging.Fields{
			"node_id":   nodeID,
			"stream_id": streamID,
		}).Warn("Retract skipped: lastSent entry missing internal_name (unexpected)")
		return nil
	}
	return SendRetractManagedStream(nodeID, &pb.RetractManagedStream{
		Name:     internalName,
		StreamId: streamID,
	})
}

// connectedNodesInCluster returns the node IDs of every Helmsman currently
// healthy in the given cluster, cluster-wide when Redis HA state is
// available. Authority model matches eligibleNodesAcrossClusters: Redis
// configured-and-reachable is authoritative; Redis configured-and-down
// returns nil (skip this tick) to avoid recomputing diffs against a
// partial local view; Redis unconfigured walks the local registry.
//
// Cluster-wide enumeration is the placement source of truth in HA: each
// Foghorn writes its connected nodes into Redis via state.NodeState, so
// every Foghorn computes placement against the same node set. Without
// this, two Foghorns serving the same cluster would each elect from
// their partial local registry and either duplicate or miss placement.
func connectedNodesInCluster(clusterID string) []string {
	if rs := GetRedisStore(); rs != nil {
		nodes, err := rs.GetAllNodes()
		if err != nil {
			// Authoritative store unreachable — skip this tick.
			return nil
		}
		out := make([]string, 0, len(nodes))
		for _, n := range nodes {
			if n == nil {
				continue
			}
			if n.ClusterID != clusterID {
				continue
			}
			if !n.IsHealthy || n.IsStale {
				continue
			}
			if n.NodeID == "" {
				continue
			}
			out = append(out, n.NodeID)
		}
		return out
	}
	if registry == nil {
		return nil
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	out := make([]string, 0, len(registry.conns))
	for _, c := range registry.conns {
		if c == nil {
			continue
		}
		if c.clusterID != clusterID {
			continue
		}
		id := c.canonicalID
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// isManagedStreamEligibleNode is the per-node eligibility predicate used when
// walking the Redis-backed node state. Kept as a pure function so the policy is
// unit-testable without standing up a Redis store.
func isManagedStreamEligibleNode(n *state.NodeState, allowedClusters map[string]struct{}) bool {
	if n == nil || n.NodeID == "" {
		return false
	}
	if !n.IsHealthy || n.IsStale {
		return false
	}
	if _, ok := allowedClusters[n.ClusterID]; !ok {
		return false
	}
	if !n.CapEdge {
		return false
	}
	return true
}

// eligibleNode is (node, cluster) — placement needs both because the
// elected node's cluster, not the reconciler's loop variable, is the
// authoritative active source for the stream.
type eligibleNode struct {
	nodeID    string
	clusterID string
}

type placementStatus int

const (
	placementOK placementStatus = iota
	placementTransient
)

// eligibleNodesAcrossClusters returns the healthy non-stale, edge-capable nodes
// in the allowed source cluster, sorted by node_id for deterministic placement.
// The input is still a slice because the DB/proto shape is shared with pull
// sources, but mist_native bootstrap/schema validation permits exactly one
// source cluster today.
//
// Edge capability gate: mist_native streams run a Mist input (e.g.
// `ts-exec:ffmpeg`) and serve playback to viewers from the same node. In
// capability-split clusters, storage-only or processing-only nodes are
// healthy but cannot run/serve a managed stream — placing one there would
// elect a node that never spawns the source. CapEdge is the "can run Mist
// playback/inputs" signal; restrict placement to edge-capable nodes.
//
// Authority model:
//   - Redis configured AND reachable: cluster-wide node set is authoritative.
//   - Redis configured AND unreachable: fail closed — return nil so the
//     reconciler tick skips placement, preserving any prior verified-applied
//     state on the elected node until Redis recovers. Falling back to the
//     local registry would split-brain: each Foghorn would elect against
//     its partial connection view and apply the same single-active stream
//     on different nodes, and would also lose the CapEdge / IsStale filters
//     the registry doesn't carry. Next tick retries.
//   - Redis NOT configured (single-Foghorn / dev): walk the local registry.
//     Mixed-capability clusters aren't deployed in that topology, so the
//     missing CapEdge filter is acceptable there.
func eligibleNodesAcrossClusters(allowed []string) []eligibleNode {
	nodes, _ := eligibleNodesAcrossClustersStatus(allowed)
	return nodes
}

func eligibleNodesAcrossClustersStatus(allowed []string) ([]eligibleNode, placementStatus) {
	if len(allowed) == 0 {
		return nil, placementOK
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, c := range allowed {
		allowedSet[c] = struct{}{}
	}
	if rs := GetRedisStore(); rs != nil {
		nodes, err := rs.GetAllNodes()
		if err != nil {
			// Authoritative store unreachable — skip this tick rather than
			// recompute from partial local state. See authority model above.
			return nil, placementTransient
		}
		out := make([]eligibleNode, 0)
		for _, n := range nodes {
			if !isManagedStreamEligibleNode(n, allowedSet) {
				continue
			}
			out = append(out, eligibleNode{nodeID: n.NodeID, clusterID: n.ClusterID})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].nodeID < out[j].nodeID })
		return out, placementOK
	}
	if registry == nil {
		return nil, placementOK
	}
	out := make([]eligibleNode, 0)
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for _, c := range registry.conns {
		if c == nil {
			continue
		}
		if _, ok := allowedSet[c.clusterID]; !ok {
			continue
		}
		id := c.canonicalID
		if id == "" {
			continue
		}
		out = append(out, eligibleNode{nodeID: id, clusterID: c.clusterID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].nodeID < out[j].nodeID })
	return out, placementOK
}

// managedStreamOwnsConnection reports whether this Foghorn instance holds
// the Helmsman bidi stream for nodeID. Used to ensure exactly one peer
// Foghorn acts on a multi-Foghorn same-cluster placement.
//
// Authority model (mirrors eligibleNodesAcrossClusters):
//   - Redis configured AND reachable: GetConnOwner is authoritative.
//   - Redis configured AND unreachable: fail closed — return false so this
//     Foghorn skips Apply/Retract this tick. Falling back to local-registry
//     presence would let two peers claim ownership concurrently (both see
//     a local connection or both fail the lookup) and double-dispatch the
//     same Apply, defeating the ownership filter.
//   - Redis NOT configured (single-Foghorn / dev): every connection is
//     local; local-registry presence == ownership.
func managedStreamOwnsConnectionStatus(nodeID string) (bool, placementStatus) {
	if rs := GetRedisStore(); rs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		owner, err := rs.GetConnOwner(ctx, nodeID)
		cancel()
		if err != nil {
			return false, placementTransient
		}
		if owner.InstanceID == "" {
			return false, placementOK
		}
		return owner.InstanceID == GetInstanceID(), placementOK
	}
	if registry == nil {
		return false, placementOK
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	if c, ok := registry.conns[nodeID]; ok && c != nil {
		return true, placementOK
	}
	return false, placementOK
}

// placementPickWithCluster mirrors placementPick but operates on
// (node, cluster) pairs so callers can attribute the elected cluster
// back to active_ingest_cluster_id without a second lookup.
func placementPickWithCluster(streamID string, sortedNodes []eligibleNode, count int) []eligibleNode {
	if count <= 0 || len(sortedNodes) == 0 {
		return nil
	}
	if count >= len(sortedNodes) {
		out := make([]eligibleNode, len(sortedNodes))
		copy(out, sortedNodes)
		return out
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(streamID))
	start := int(h.Sum64() % uint64(len(sortedNodes)))
	out := make([]eligibleNode, count)
	for i := range count {
		out[i] = sortedNodes[(start+i)%len(sortedNodes)]
	}
	return out
}

// placementPick deterministically picks `count` nodes from `sortedNodes`
// for a stream identified by streamID. Stable across reconciler ticks
// (same input ⇒ same picks) and across Foghorn restarts. Uses FNV-1a +
// rotation so adding/removing a node only shifts placement by 1.
func placementPick(streamID string, sortedNodes []string, count int) []string {
	if count <= 0 || len(sortedNodes) == 0 {
		return nil
	}
	if count >= len(sortedNodes) {
		out := make([]string, len(sortedNodes))
		copy(out, sortedNodes)
		return out
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(streamID))
	start := int(h.Sum64() % uint64(len(sortedNodes)))
	out := make([]string, count)
	for i := range count {
		out[i] = sortedNodes[(start+i)%len(sortedNodes)]
	}
	return out
}

// SendLocalApplyManagedStream / SendLocalRetractManagedStream deliver the
// command to a Helmsman connected to this Foghorn instance. The relay
// server dispatches received ForwardCommandRequest_ApplyManagedStream /
// _RetractManagedStream through these.
func SendLocalApplyManagedStream(nodeID string, req *pb.ApplyManagedStream) error {
	return sendLocalManagedStreamMessage(nodeID, &pb.ControlMessage{
		Payload: &pb.ControlMessage_ApplyManagedStream{ApplyManagedStream: req},
		SentAt:  timestamppb.Now(),
	})
}

func SendLocalRetractManagedStream(nodeID string, req *pb.RetractManagedStream) error {
	return sendLocalManagedStreamMessage(nodeID, &pb.ControlMessage{
		Payload: &pb.ControlMessage_RetractManagedStream{RetractManagedStream: req},
		SentAt:  timestamppb.Now(),
	})
}

func sendLocalManagedStreamMessage(nodeID string, msg *pb.ControlMessage) error {
	if registry == nil {
		return ErrNotConnected
	}
	registry.mu.RLock()
	c := registry.conns[nodeID]
	registry.mu.RUnlock()
	if c == nil {
		return ErrNotConnected
	}
	return c.stream.Send(msg)
}

// SendApplyManagedStream tries the local registry first, then forwards
// via commandRelay if a peer Foghorn owns the connection. Mirrors the
// SendClipPull / SendDVRStart pattern so multi-Foghorn-per-cluster works.
func SendApplyManagedStream(nodeID string, req *pb.ApplyManagedStream) error {
	err := SendLocalApplyManagedStream(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_ApplyManagedStream{ApplyManagedStream: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}

func SendRetractManagedStream(nodeID string, req *pb.RetractManagedStream) error {
	err := SendLocalRetractManagedStream(nodeID, req)
	if !shouldRelay(nodeID, err) {
		return err
	}
	if commandRelay == nil {
		return ErrNotConnected
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if relayErr := commandRelay.forward(ctx, &pb.ForwardCommandRequest{
		TargetNodeId: nodeID,
		Command:      &pb.ForwardCommandRequest_RetractManagedStream{RetractManagedStream: req},
	}); relayErr != nil {
		return relayFailure(err, relayErr)
	}
	return nil
}
