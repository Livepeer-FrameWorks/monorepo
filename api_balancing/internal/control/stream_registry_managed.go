package control

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"maps"
	"sync"
)

// managedState holds the reconciler-side bookkeeping for managed
// (mist-native) streams: what Foghorn last told each node to Apply, and
// what each node's sidecar last reported applied. Owned by StreamRegistry
// so the registry is the single home for source-stream state.
type managedState struct {
	mu sync.Mutex
	// clusterID → nodeID → streamID → last Apply snapshot Foghorn emitted.
	// Cluster scope prevents a tick for cluster A from retracting streams
	// belonging to a different served cluster.
	lastSent map[string]map[string]map[string]managedStreamSnapshot
	// nodeID → streamID → snapshot from the most recent sidecar Heartbeat.
	// Apply-key match against the desired snapshot decides whether the
	// reconciler must re-emit Apply.
	verified map[string]map[string]managedStreamSnapshot
}

func newManagedState() *managedState {
	return &managedState{
		lastSent: make(map[string]map[string]map[string]managedStreamSnapshot),
		verified: make(map[string]map[string]managedStreamSnapshot),
	}
}

// ManagedSetLastSent records the Apply snapshot Foghorn just emitted for
// (clusterID, nodeID, streamID).
func (r *StreamRegistry) ManagedSetLastSent(clusterID, nodeID, streamID string, snap managedStreamSnapshot) {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		cluster = make(map[string]map[string]managedStreamSnapshot)
		r.managed.lastSent[clusterID] = cluster
	}
	node := cluster[nodeID]
	if node == nil {
		node = make(map[string]managedStreamSnapshot)
		cluster[nodeID] = node
	}
	node[streamID] = snap
}

// ManagedGetLastSent returns the recorded Apply snapshot for
// (clusterID, nodeID, streamID), or zero+false.
func (r *StreamRegistry) ManagedGetLastSent(clusterID, nodeID, streamID string) (managedStreamSnapshot, bool) {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	if cluster := r.managed.lastSent[clusterID]; cluster != nil {
		if node := cluster[nodeID]; node != nil {
			snap, ok := node[streamID]
			return snap, ok
		}
	}
	return managedStreamSnapshot{}, false
}

// ManagedDeleteLastSent drops the recorded Apply snapshot for
// (clusterID, nodeID, streamID). Cleans up empty parent maps.
func (r *StreamRegistry) ManagedDeleteLastSent(clusterID, nodeID, streamID string) {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return
	}
	node := cluster[nodeID]
	if node == nil {
		return
	}
	delete(node, streamID)
	if len(node) == 0 {
		delete(cluster, nodeID)
		if len(cluster) == 0 {
			delete(r.managed.lastSent, clusterID)
		}
	}
}

// ManagedListLastSent returns a copy of the per-stream Apply snapshots
// recorded for (clusterID, nodeID). Returns nil if the node has no
// recorded entries.
func (r *StreamRegistry) ManagedListLastSent(clusterID, nodeID string) map[string]managedStreamSnapshot {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return nil
	}
	node := cluster[nodeID]
	if node == nil {
		return nil
	}
	out := make(map[string]managedStreamSnapshot, len(node))
	maps.Copy(out, node)
	return out
}

// ManagedSnapshotCluster returns a deep copy of every node's Apply
// snapshot map in clusterID. Used by the reconciler to read a consistent
// per-tick view without holding the registry lock across per-stream work.
func (r *StreamRegistry) ManagedSnapshotCluster(clusterID string) map[string]map[string]managedStreamSnapshot {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return nil
	}
	out := make(map[string]map[string]managedStreamSnapshot, len(cluster))
	for nodeID, streams := range cluster {
		cp := make(map[string]managedStreamSnapshot, len(streams))
		maps.Copy(cp, streams)
		out[nodeID] = cp
	}
	return out
}

// ManagedListClusterNodes returns the node IDs that currently have any
// Apply snapshots recorded for the given cluster.
func (r *StreamRegistry) ManagedListClusterNodes(clusterID string) []string {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return nil
	}
	out := make([]string, 0, len(cluster))
	for nodeID := range cluster {
		out = append(out, nodeID)
	}
	return out
}

// ManagedForgetNode clears every Apply snapshot recorded for a node
// across all clusters plus the verified-applied set for that node.
// Called on Helmsman disconnect so the next reconciler tick re-emits
// Apply for whatever should be on that node.
func (r *StreamRegistry) ManagedForgetNode(nodeID string) {
	r.managed.mu.Lock()
	for clusterID, nodes := range r.managed.lastSent {
		delete(nodes, nodeID)
		if len(nodes) == 0 {
			delete(r.managed.lastSent, clusterID)
		}
	}
	delete(r.managed.verified, nodeID)
	r.managed.mu.Unlock()
}

// ManagedSetVerifiedFromHeartbeat replaces this node's verified-applied
// set with the snapshot the sidecar just reported.
func (r *StreamRegistry) ManagedSetVerifiedFromHeartbeat(nodeID string, applied []*ipcpb.AppliedManagedStream) {
	if nodeID == "" {
		return
	}
	set := make(map[string]managedStreamSnapshot, len(applied))
	for _, a := range applied {
		sid := a.GetStreamId()
		if sid == "" {
			continue
		}
		set[sid] = managedStreamSnapshot{
			sourceSpec:   a.GetSource(),
			alwaysOn:     a.GetAlwaysOn(),
			ingestMode:   a.GetIngestMode(),
			internalName: a.GetName(),
		}
	}
	r.managed.mu.Lock()
	if len(set) == 0 {
		delete(r.managed.verified, nodeID)
	} else {
		r.managed.verified[nodeID] = set
	}
	r.managed.mu.Unlock()
}

// ManagedVerifiedMatches reports whether the sidecar's heartbeat snapshot
// for (nodeID, streamID) has the same apply key as the desired snapshot.
// False when missing or when any apply-key field differs.
func (r *StreamRegistry) ManagedVerifiedMatches(nodeID, streamID string, desired managedStreamSnapshot) bool {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	set, ok := r.managed.verified[nodeID]
	if !ok {
		return false
	}
	got, ok := set[streamID]
	if !ok {
		return false
	}
	return got.applyKey() == desired.applyKey()
}

// ManagedVerifiedPresent reports presence-only: any record of the stream
// in the sidecar's applied set, regardless of which version.
func (r *StreamRegistry) ManagedVerifiedPresent(nodeID, streamID string) bool {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	set, ok := r.managed.verified[nodeID]
	if !ok {
		return false
	}
	_, ok = set[streamID]
	return ok
}

// ManagedHydrateForNode seeds lastSent with the sidecar's reported
// applied set on (re)connect, parked under a pending-cluster sentinel
// until the next reconciler tick promotes it to the real cluster bucket.
func (r *StreamRegistry) ManagedHydrateForNode(nodeID string, applied []*ipcpb.AppliedManagedStream) {
	if len(applied) == 0 {
		return
	}
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	pending := r.managed.lastSent[managedStreamPendingClusterKey]
	if pending == nil {
		pending = make(map[string]map[string]managedStreamSnapshot)
		r.managed.lastSent[managedStreamPendingClusterKey] = pending
	}
	nodeMap := pending[nodeID]
	if nodeMap == nil {
		nodeMap = make(map[string]managedStreamSnapshot)
		pending[nodeID] = nodeMap
	}
	for _, a := range applied {
		streamID := a.GetStreamId()
		if streamID == "" {
			continue
		}
		nodeMap[streamID] = managedStreamSnapshot{
			sourceSpec:   a.GetSource(),
			alwaysOn:     a.GetAlwaysOn(),
			ingestMode:   a.GetIngestMode(),
			internalName: a.GetName(),
		}
	}
}

// ManagedAdoptPending migrates pending hydrated entries for nodes in the
// given cluster into clusterID's bucket. Existing entries win on conflict.
func (r *StreamRegistry) ManagedAdoptPending(clusterID string, clusterNodes []string) {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	pending := r.managed.lastSent[managedStreamPendingClusterKey]
	if pending == nil {
		return
	}
	nodesInCluster := make(map[string]struct{}, len(clusterNodes))
	for _, n := range clusterNodes {
		nodesInCluster[n] = struct{}{}
	}
	for nodeID, streams := range pending {
		if _, ok := nodesInCluster[nodeID]; !ok {
			continue
		}
		bucket := r.managed.lastSent[clusterID]
		if bucket == nil {
			bucket = make(map[string]map[string]managedStreamSnapshot)
			r.managed.lastSent[clusterID] = bucket
		}
		if existing, ok := bucket[nodeID]; ok {
			for sid, snap := range streams {
				if _, present := existing[sid]; !present {
					existing[sid] = snap
				}
			}
		} else {
			bucket[nodeID] = streams
		}
		delete(pending, nodeID)
	}
	if len(pending) == 0 {
		delete(r.managed.lastSent, managedStreamPendingClusterKey)
	}
}

// ManagedReset clears all managed-stream state. Test helper.
func (r *StreamRegistry) ManagedReset() {
	r.managed.mu.Lock()
	r.managed.lastSent = make(map[string]map[string]map[string]managedStreamSnapshot)
	r.managed.verified = make(map[string]map[string]managedStreamSnapshot)
	r.managed.mu.Unlock()
}

// ManagedRetractCluster removes every Apply snapshot in clusterID,
// returning the (nodeID, streamID, internalName) tuples so the reconciler
// can emit matching Retract events. Used when a cluster's entire desired
// set goes away (rare; mostly multi-cluster Foghorn transitions).
func (r *StreamRegistry) ManagedRetractCluster(clusterID string) map[string]map[string]managedStreamSnapshot {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return nil
	}
	out := make(map[string]map[string]managedStreamSnapshot, len(cluster))
	for nodeID, streams := range cluster {
		copied := make(map[string]managedStreamSnapshot, len(streams))
		maps.Copy(copied, streams)
		out[nodeID] = copied
	}
	delete(r.managed.lastSent, clusterID)
	return out
}

// ManagedRetractClusterNode removes every Apply snapshot for
// (clusterID, nodeID) and returns them so the reconciler can emit Retract.
func (r *StreamRegistry) ManagedRetractClusterNode(clusterID, nodeID string) map[string]managedStreamSnapshot {
	r.managed.mu.Lock()
	defer r.managed.mu.Unlock()
	cluster := r.managed.lastSent[clusterID]
	if cluster == nil {
		return nil
	}
	streams := cluster[nodeID]
	if streams == nil {
		return nil
	}
	out := make(map[string]managedStreamSnapshot, len(streams))
	maps.Copy(out, streams)
	delete(cluster, nodeID)
	if len(cluster) == 0 {
		delete(r.managed.lastSent, clusterID)
	}
	return out
}
