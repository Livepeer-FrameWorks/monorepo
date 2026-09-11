package balancer

import (
	"errors"
	"slices"
	"time"

	"frameworks/api_balancing/internal/state"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

var ErrPlacementInventory = errors.New("placement inventory is incomplete or inconsistent")

type PlacementInventorySnapshot struct {
	Snapshot   *state.BalancerSnapshot
	Complete   bool
	ExpiresAt  time.Time
	tenantID   string
	clusters   map[string]bool
	observedAt time.Time
}

// Observe binds candidate lifetime and pool completeness to the membership
// snapshot. Telemetry cannot refresh membership or drop an authorized cluster.
func (inventory *PlacementInventorySnapshot) Observe(req PlacementObservationRequest) (PlacementCellObservation, error) {
	return inventory.observe(req, false)
}

// ObserveCapacity preserves membership completeness for a source-free preview.
func (inventory *PlacementInventorySnapshot) ObserveCapacity(req PlacementObservationRequest) (PlacementCellObservation, error) {
	return inventory.observe(req, true)
}

func (inventory *PlacementInventorySnapshot) observe(req PlacementObservationRequest, capacityOnly bool) (PlacementCellObservation, error) {
	if inventory == nil || req.TenantID != inventory.tenantID || req.Now.Before(inventory.observedAt) || !req.Now.Before(inventory.ExpiresAt) || len(req.Clusters) != len(inventory.clusters) {
		return PlacementCellObservation{}, ErrPlacementInventory
	}
	for clusterID := range inventory.clusters {
		if _, exists := req.Clusters[clusterID]; !exists {
			return PlacementCellObservation{}, ErrPlacementInventory
		}
	}
	req.Snapshot = inventory.Snapshot
	candidates, err := observePlacementNodes(req, capacityOnly)
	if err != nil {
		return PlacementCellObservation{}, err
	}
	for index := range candidates {
		if inventory.ExpiresAt.Before(candidates[index].ExpiresAt) {
			candidates[index].ExpiresAt = inventory.ExpiresAt
		}
	}
	return PlacementCellObservation{Candidates: candidates, Complete: inventory.Complete, ObservedAt: req.Now, ExpiresAt: inventory.ExpiresAt}, nil
}

// ReconcilePlacementInventory joins authoritative membership to runtime telemetry.
// Missing members retain an unavailable placeholder; unregistered runtime nodes
// cannot enter a pool. Administrative state never fabricates health or capacity.
func ReconcilePlacementInventory(tenantID string, cell PlacementCell, inventory *quartermasterpb.MediaPlacementInventory, observed *state.BalancerSnapshot, now time.Time) (*PlacementInventorySnapshot, error) {
	if tenantID == "" || now.IsZero() || observed == nil || inventory == nil || !inventory.GetComplete() || inventory.GetTenantId() != tenantID || inventory.GetControlCellId() != cell.ID || len(cell.ClusterIDs) == 0 || len(cell.ClusterIDs) > 4096 || len(inventory.GetClusterIds()) != len(cell.ClusterIDs) || len(inventory.GetNodes()) > 4096 {
		return nil, ErrPlacementInventory
	}
	stamp := inventory.GetObservedAt()
	if stamp == nil || !stamp.IsValid() || stamp.AsTime().After(now) || !now.Before(stamp.AsTime().Add(placementObservationLifetime)) {
		return nil, ErrPlacementInventory
	}
	clusters := make(map[string]bool, len(cell.ClusterIDs))
	for _, clusterID := range cell.ClusterIDs {
		if clusterID == "" || clusters[clusterID] {
			return nil, ErrPlacementInventory
		}
		clusters[clusterID] = true
	}
	seenClusters := make(map[string]bool, len(clusters))
	for _, clusterID := range inventory.GetClusterIds() {
		if !clusters[clusterID] || seenClusters[clusterID] {
			return nil, ErrPlacementInventory
		}
		seenClusters[clusterID] = true
	}
	runtime := make(map[string]state.EnhancedBalancerNodeSnapshot)
	for _, node := range observed.Nodes {
		if !clusters[node.ClusterID] {
			continue
		}
		if node.NodeID == "" {
			return nil, ErrPlacementInventory
		}
		if _, exists := runtime[node.NodeID]; exists {
			return nil, ErrPlacementInventory
		}
		runtime[node.NodeID] = node
		if len(runtime) > 4096 {
			return nil, ErrPlacementInventory
		}
	}
	out := &state.BalancerSnapshot{Timestamp: observed.Timestamp, Nodes: make([]state.EnhancedBalancerNodeSnapshot, 0, len(inventory.GetNodes()))}
	seenNodes := make(map[string]bool, len(inventory.GetNodes()))
	for _, member := range inventory.GetNodes() {
		if member == nil || member.GetNodeId() == "" || !clusters[member.GetClusterId()] || seenNodes[member.GetNodeId()] {
			return nil, ErrPlacementInventory
		}
		seenNodes[member.GetNodeId()] = true
		node, found := runtime[member.GetNodeId()]
		if found && node.ClusterID != member.GetClusterId() {
			return nil, ErrPlacementInventory
		}
		if !found {
			node = state.EnhancedBalancerNodeSnapshot{NodeID: member.GetNodeId(), ClusterID: member.GetClusterId()}
		}
		if !member.GetAdmissionEnabled() {
			node.IsActive = false
		}
		out.Nodes = append(out.Nodes, node)
	}
	// Unregistered runtime nodes cannot be selected. Snapshot skew prevents
	// proving pool exhaustion, but does not invalidate healthy registered nodes.
	complete := true
	for nodeID := range runtime {
		if !seenNodes[nodeID] {
			complete = false
		}
	}
	slices.SortFunc(out.Nodes, func(a, b state.EnhancedBalancerNodeSnapshot) int {
		if a.ClusterID < b.ClusterID || (a.ClusterID == b.ClusterID && a.NodeID < b.NodeID) {
			return -1
		}
		if a.ClusterID == b.ClusterID && a.NodeID == b.NodeID {
			return 0
		}
		return 1
	})
	return &PlacementInventorySnapshot{Snapshot: out, Complete: complete, ExpiresAt: stamp.AsTime().Add(placementObservationLifetime), tenantID: tenantID, clusters: clusters, observedAt: stamp.AsTime()}, nil
}
