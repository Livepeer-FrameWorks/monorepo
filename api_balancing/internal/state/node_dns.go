package state

import (
	"net"
	"net/url"
	"sync"
	"time"
)

// NodeDNSSnapshot is the subset of NodeState that affects DNS visibility.
// Quartermaster uses this payload to decide whether the (cluster, edge-*)
// record set for a node should change.
type NodeDNSSnapshot struct {
	NodeID        string
	IsHealthy     bool
	ClusterID     string
	ExternalIP    string // IP literal only; empty when Foghorn cannot derive one
	CapIngest     bool
	CapEdge       bool
	CapStorage    bool
	CapProcessing bool
}

func (a NodeDNSSnapshot) equals(b NodeDNSSnapshot) bool {
	return a.NodeID == b.NodeID &&
		a.IsHealthy == b.IsHealthy &&
		a.ClusterID == b.ClusterID &&
		a.ExternalIP == b.ExternalIP &&
		a.CapIngest == b.CapIngest &&
		a.CapEdge == b.CapEdge &&
		a.CapStorage == b.CapStorage &&
		a.CapProcessing == b.CapProcessing
}

// dnsDeltaTracker is embedded in StreamStateManager. It records the last
// snapshot reported upstream per node and a dirty set of nodes whose current
// state may differ.
type dnsDeltaTracker struct {
	mu            sync.Mutex
	lastPublished map[string]NodeDNSSnapshot
	dirty         map[string]struct{}
}

func newDNSDeltaTracker() dnsDeltaTracker {
	return dnsDeltaTracker{
		lastPublished: make(map[string]NodeDNSSnapshot),
		dirty:         make(map[string]struct{}),
	}
}

// MarkNodeDNSChanged signals that node_id's DNS-relevant state may have
// changed since the last publish. The coalescer diffs against the last
// published snapshot before emitting.
func (sm *StreamStateManager) MarkNodeDNSChanged(nodeID string) {
	if nodeID == "" {
		return
	}
	sm.dnsDelta.mu.Lock()
	sm.dnsDelta.dirty[nodeID] = struct{}{}
	sm.dnsDelta.mu.Unlock()
}

// snapshotNodeDNSLocked builds the DNS snapshot for a node from its current
// state. Caller must hold sm.mu for read.
func (sm *StreamStateManager) snapshotNodeDNSLocked(nodeID string) (NodeDNSSnapshot, bool) {
	n := sm.nodes[nodeID]
	if n == nil {
		return NodeDNSSnapshot{}, false
	}
	return NodeDNSSnapshot{
		NodeID:        n.NodeID,
		IsHealthy:     n.IsHealthy,
		ClusterID:     n.ClusterID,
		ExternalIP:    extractIPFromBaseURL(n.BaseURL),
		CapIngest:     n.CapIngest,
		CapEdge:       n.CapEdge,
		CapStorage:    n.CapStorage,
		CapProcessing: n.CapProcessing,
	}, true
}

// SnapshotNodeDNS returns the current DNS snapshot for a node.
func (sm *StreamStateManager) SnapshotNodeDNS(nodeID string) (NodeDNSSnapshot, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.snapshotNodeDNSLocked(nodeID)
}

// ConsumeDNSRelevantDeltas drains the dirty set and returns the snapshot of
// every node whose current DNS-relevant state differs from its last published
// snapshot. After return, the last-published map is advanced to the values
// reported, so the same delta is not emitted twice.
//
// A node that is dirty but whose snapshot matches the last publish is dropped
// silently (coalesced flap).
//
// A node that was previously published but is now missing from sm.nodes
// emits a synthetic unhealthy snapshot with the last known cluster, so
// disconnects published before MarkNodeDisconnected still trigger removal.
func (sm *StreamStateManager) ConsumeDNSRelevantDeltas() []NodeDNSSnapshot {
	sm.dnsDelta.mu.Lock()
	if len(sm.dnsDelta.dirty) == 0 {
		sm.dnsDelta.mu.Unlock()
		return nil
	}
	dirty := sm.dnsDelta.dirty
	sm.dnsDelta.dirty = make(map[string]struct{}, len(dirty))
	sm.dnsDelta.mu.Unlock()

	sm.mu.RLock()
	out := make([]NodeDNSSnapshot, 0, len(dirty))
	tombstones := make([]NodeDNSSnapshot, 0)
	for nodeID := range dirty {
		snap, ok := sm.snapshotNodeDNSLocked(nodeID)
		if !ok {
			tombstones = append(tombstones, NodeDNSSnapshot{NodeID: nodeID, IsHealthy: false})
			continue
		}
		out = append(out, snap)
	}
	sm.mu.RUnlock()

	sm.dnsDelta.mu.Lock()
	final := out[:0]
	for _, snap := range out {
		prev, had := sm.dnsDelta.lastPublished[snap.NodeID]
		if had && prev.equals(snap) {
			continue
		}
		sm.dnsDelta.lastPublished[snap.NodeID] = snap
		final = append(final, snap)
	}
	for _, snap := range tombstones {
		prev, had := sm.dnsDelta.lastPublished[snap.NodeID]
		if had && prev.ClusterID != "" {
			snap.ClusterID = prev.ClusterID
		}
		if had && prev.equals(snap) {
			continue
		}
		sm.dnsDelta.lastPublished[snap.NodeID] = snap
		final = append(final, snap)
	}
	sm.dnsDelta.mu.Unlock()

	return final
}

// AllReportedNodes returns the DNS snapshot of every node currently in the
// state manager — healthy and unhealthy — so the 60s repair loop can converge
// QM on disconnect tombstones the fast delta path may have missed.
// Nodes whose LastUpdate is older than staleAfter (zero = no filter) are
// skipped: state we've never confirmed shouldn't be republished as fact.
func (sm *StreamStateManager) AllReportedNodes(staleAfter time.Duration) []NodeDNSSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	cutoff := time.Time{}
	if staleAfter > 0 {
		cutoff = time.Now().Add(-staleAfter)
	}
	out := make([]NodeDNSSnapshot, 0, len(sm.nodes))
	for id, n := range sm.nodes {
		if !cutoff.IsZero() && n.LastUpdate.Before(cutoff) {
			continue
		}
		if snap, ok := sm.snapshotNodeDNSLocked(id); ok {
			out = append(out, snap)
		}
	}
	return out
}

func extractIPFromBaseURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}
