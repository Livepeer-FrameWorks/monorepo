package balancer

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"frameworks/api_balancing/internal/state"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

var ErrPlacementInventory = errors.New("placement inventory is incomplete or inconsistent")

// Placement inventory refusal reasons. Each names one concrete condition so a
// refusal is diagnosable from the log line alone.
const (
	InventoryReasonMissingInput         = "missing_input"
	InventoryReasonIncomplete           = "incomplete"
	InventoryReasonTenantMismatch       = "tenant_mismatch"
	InventoryReasonCellMismatch         = "cell_mismatch"
	InventoryReasonClusterSetMismatch   = "cluster_set_mismatch"
	InventoryReasonOversize             = "oversize"
	InventoryReasonStampInvalid         = "stamp_invalid"
	InventoryReasonStampSkew            = "stamp_skew"
	InventoryReasonExpired              = "expired"
	InventoryReasonMemberInvalid        = "member_invalid"
	InventoryReasonNodeClusterMismatch  = "node_cluster_mismatch"
	InventoryReasonDuplicateRuntimeNode = "duplicate_runtime_node"
)

// PlacementInventoryError is a refusal with its concrete reason. It matches
// ErrPlacementInventory under errors.Is.
type PlacementInventoryError struct {
	Reason string
	Detail string
}

func (e *PlacementInventoryError) Error() string {
	if e.Detail == "" {
		return "placement inventory " + e.Reason
	}
	return "placement inventory " + e.Reason + ": " + e.Detail
}

func (e *PlacementInventoryError) Is(target error) bool { return target == ErrPlacementInventory }

func inventoryError(reason, format string, args ...any) error {
	return &PlacementInventoryError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// placementInventoryMaxStampSkew bounds how far Quartermaster's stamp may be from
// the local read instant before the clocks are treated as broken. Smaller skew is
// normal NTP drift between hosts and never refuses placement.
const placementInventoryMaxStampSkew = placementObservationLifetime

type PlacementInventorySnapshot struct {
	Snapshot  *state.BalancerSnapshot
	Complete  bool
	ExpiresAt time.Time
	// StampSkew is Quartermaster's membership stamp minus the local read instant.
	StampSkew time.Duration
	// UnregisteredNodes are runtime nodes in the cell's clusters that membership
	// does not list. They cannot be selected.
	UnregisteredNodes []string
	tenantID          string
	clusters          map[string]bool
	observedAt        time.Time
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
	if inventory == nil {
		return PlacementCellObservation{}, inventoryError(InventoryReasonMissingInput, "no inventory snapshot")
	}
	if req.TenantID != inventory.tenantID {
		return PlacementCellObservation{}, inventoryError(InventoryReasonTenantMismatch, "request tenant %q, inventory tenant %q", req.TenantID, inventory.tenantID)
	}
	if req.Now.Before(inventory.observedAt) || !req.Now.Before(inventory.ExpiresAt) {
		return PlacementCellObservation{}, inventoryError(InventoryReasonExpired, "now %s outside [%s, %s)", req.Now.Format(time.RFC3339Nano), inventory.observedAt.Format(time.RFC3339Nano), inventory.ExpiresAt.Format(time.RFC3339Nano))
	}
	if len(req.Clusters) != len(inventory.clusters) {
		return PlacementCellObservation{}, inventoryError(InventoryReasonClusterSetMismatch, "request has %d clusters, inventory %d", len(req.Clusters), len(inventory.clusters))
	}
	for clusterID := range inventory.clusters {
		if _, exists := req.Clusters[clusterID]; !exists {
			return PlacementCellObservation{}, inventoryError(InventoryReasonClusterSetMismatch, "request lacks inventory cluster %q", clusterID)
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
//
// readAt is the local instant taken before membership was requested. The
// membership lifetime is anchored to it, so Quartermaster's clock only feeds a
// skew report and never decides freshness.
func ReconcilePlacementInventory(tenantID string, cell PlacementCell, inventory *quartermasterpb.MediaPlacementInventory, observed *state.BalancerSnapshot, readAt time.Time) (*PlacementInventorySnapshot, error) {
	switch {
	case tenantID == "" || readAt.IsZero() || observed == nil || inventory == nil:
		return nil, inventoryError(InventoryReasonMissingInput, "tenant=%t read_at=%t snapshot=%t membership=%t", tenantID != "", !readAt.IsZero(), observed != nil, inventory != nil)
	case !inventory.GetComplete():
		return nil, inventoryError(InventoryReasonIncomplete, "quartermaster returned incomplete membership")
	case inventory.GetTenantId() != tenantID:
		return nil, inventoryError(InventoryReasonTenantMismatch, "membership tenant %q, requested %q", inventory.GetTenantId(), tenantID)
	case inventory.GetControlCellId() != cell.ID:
		return nil, inventoryError(InventoryReasonCellMismatch, "membership cell %q, requested %q", inventory.GetControlCellId(), cell.ID)
	case len(cell.ClusterIDs) == 0 || len(cell.ClusterIDs) > 4096 || len(inventory.GetClusterIds()) != len(cell.ClusterIDs):
		return nil, inventoryError(InventoryReasonClusterSetMismatch, "requested %d clusters, membership has %d", len(cell.ClusterIDs), len(inventory.GetClusterIds()))
	case len(inventory.GetNodes()) > 4096:
		return nil, inventoryError(InventoryReasonOversize, "%d membership nodes", len(inventory.GetNodes()))
	}
	stamp := inventory.GetObservedAt()
	if stamp == nil || !stamp.IsValid() {
		return nil, inventoryError(InventoryReasonStampInvalid, "membership stamp missing or invalid")
	}
	skew := stamp.AsTime().Sub(readAt)
	if skew >= placementInventoryMaxStampSkew || skew <= -placementInventoryMaxStampSkew {
		return nil, inventoryError(InventoryReasonStampSkew, "quartermaster stamp %s is %s from local read instant %s", stamp.AsTime().Format(time.RFC3339Nano), skew, readAt.Format(time.RFC3339Nano))
	}
	clusters := make(map[string]bool, len(cell.ClusterIDs))
	for _, clusterID := range cell.ClusterIDs {
		if clusterID == "" || clusters[clusterID] {
			return nil, inventoryError(InventoryReasonClusterSetMismatch, "requested cluster %q is empty or duplicated", clusterID)
		}
		clusters[clusterID] = true
	}
	seenClusters := make(map[string]bool, len(clusters))
	for _, clusterID := range inventory.GetClusterIds() {
		if !clusters[clusterID] || seenClusters[clusterID] {
			return nil, inventoryError(InventoryReasonClusterSetMismatch, "membership cluster %q is not requested or duplicated", clusterID)
		}
		seenClusters[clusterID] = true
	}
	runtime := make(map[string]state.EnhancedBalancerNodeSnapshot)
	for _, node := range observed.Nodes {
		if !clusters[node.ClusterID] {
			continue
		}
		if node.NodeID == "" {
			return nil, inventoryError(InventoryReasonDuplicateRuntimeNode, "runtime node without id in cluster %q", node.ClusterID)
		}
		if _, exists := runtime[node.NodeID]; exists {
			return nil, inventoryError(InventoryReasonDuplicateRuntimeNode, "runtime node %q appears twice", node.NodeID)
		}
		runtime[node.NodeID] = node
		if len(runtime) > 4096 {
			return nil, inventoryError(InventoryReasonOversize, "more than 4096 runtime nodes")
		}
	}
	out := &state.BalancerSnapshot{Timestamp: observed.Timestamp, Nodes: make([]state.EnhancedBalancerNodeSnapshot, 0, len(inventory.GetNodes()))}
	seenNodes := make(map[string]bool, len(inventory.GetNodes()))
	for index, member := range inventory.GetNodes() {
		if member == nil || member.GetNodeId() == "" || !clusters[member.GetClusterId()] || seenNodes[member.GetNodeId()] {
			return nil, inventoryError(InventoryReasonMemberInvalid, "membership entry %d (node %q, cluster %q) is empty, outside the cell, or duplicated", index, member.GetNodeId(), member.GetClusterId())
		}
		seenNodes[member.GetNodeId()] = true
		node, found := runtime[member.GetNodeId()]
		if found && node.ClusterID != member.GetClusterId() {
			return nil, inventoryError(InventoryReasonNodeClusterMismatch, "node %q reports cluster %q, membership lists cluster %q", member.GetNodeId(), node.ClusterID, member.GetClusterId())
		}
		if !found {
			node = state.EnhancedBalancerNodeSnapshot{NodeID: member.GetNodeId(), ClusterID: member.GetClusterId()}
		}
		if !member.GetAdmissionEnabled() {
			node.IsActive = false
		}
		out.Nodes = append(out.Nodes, node)
	}
	// A runtime node membership does not list is never selectable, so it neither
	// adds capacity nor blocks spillover; it is reported for drift diagnosis.
	var unregistered []string
	for nodeID := range runtime {
		if !seenNodes[nodeID] {
			unregistered = append(unregistered, nodeID)
		}
	}
	slices.Sort(unregistered)
	slices.SortFunc(out.Nodes, func(a, b state.EnhancedBalancerNodeSnapshot) int {
		if a.ClusterID < b.ClusterID || (a.ClusterID == b.ClusterID && a.NodeID < b.NodeID) {
			return -1
		}
		if a.ClusterID == b.ClusterID && a.NodeID == b.NodeID {
			return 0
		}
		return 1
	})
	return &PlacementInventorySnapshot{
		Snapshot: out, Complete: true, ExpiresAt: readAt.Add(placementObservationLifetime), StampSkew: skew, UnregisteredNodes: unregistered,
		tenantID: tenantID, clusters: clusters, observedAt: readAt,
	}, nil
}
