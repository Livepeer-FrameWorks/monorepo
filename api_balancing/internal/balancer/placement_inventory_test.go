package balancer

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func inventoryFixture() (PlacementCell, *quartermasterpb.MediaPlacementInventory, PlacementObservationRequest) {
	req := placementObservationFixture()
	cell := PlacementCell{ID: "cell", ClusterIDs: []string{"private", "empty"}}
	inventory := &quartermasterpb.MediaPlacementInventory{
		TenantId: req.TenantID, ControlCellId: cell.ID, ClusterIds: []string{"empty", "private"},
		Complete: true, ObservedAt: timestamppb.New(req.Now.Add(-2 * time.Second)),
		Nodes: []*quartermasterpb.MediaPlacementInventoryNode{
			{ClusterId: "private", NodeId: "node", AdmissionEnabled: true},
			{ClusterId: "private", NodeId: "missing", AdmissionEnabled: true},
		},
	}
	return cell, inventory, req
}

func TestReconcilePlacementInventoryPreservesMissingAndDisabledMembers(t *testing.T) {
	cell, inventory, req := inventoryFixture()
	inventory.Nodes[0].AdmissionEnabled = false
	original := req.Snapshot.Nodes[0]
	joined, err := ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now)
	if err != nil || !joined.Complete || len(joined.Snapshot.Nodes) != 2 || !joined.ExpiresAt.Equal(req.Now.Add(28*time.Second)) {
		t.Fatalf("reconcile: %+v, %v", joined, err)
	}
	missing, disabled := joined.Snapshot.Nodes[0], joined.Snapshot.Nodes[1]
	if missing.NodeID != "missing" || missing.IsActive || !missing.MetricsObservedAt.IsZero() || !missing.LastHeartbeat.IsZero() || missing.BWLimit != 0 {
		t.Fatalf("missing telemetry fabricated: %+v", missing)
	}
	if disabled.NodeID != "node" || disabled.IsActive || disabled.BWLimit != original.BWLimit || !disabled.MetricsObservedAt.Equal(original.MetricsObservedAt) {
		t.Fatalf("administrative disable lost or rewrote telemetry: %+v", disabled)
	}
	if !reflect.DeepEqual(req.Snapshot.Nodes[0], original) {
		t.Fatal("reconciliation mutated runtime state")
	}
}

func TestReconcilePlacementInventorySkewPreservesHealthyNodesWithoutCompleteness(t *testing.T) {
	cell, inventory, req := inventoryFixture()
	extra := req.Snapshot.Nodes[0]
	extra.NodeID = "unregistered"
	req.Snapshot.Nodes = append(req.Snapshot.Nodes, extra)
	joined, err := ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now)
	if err != nil || joined.Complete || len(joined.Snapshot.Nodes) != 2 || !joined.Snapshot.Nodes[1].IsActive {
		t.Fatalf("skew discarded authorized healthy capacity: %+v, %v", joined, err)
	}
	for _, node := range joined.Snapshot.Nodes {
		if node.NodeID == extra.NodeID {
			t.Fatal("unregistered node entered candidate pool")
		}
	}
	req.Snapshot.Nodes[1].ClusterID = "not-entitled"
	joined, err = ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now)
	if err != nil || !joined.Complete {
		t.Fatalf("unrelated cluster affected completeness: %+v, %v", joined, err)
	}
}

func TestReconcilePlacementInventoryRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*PlacementCell, *quartermasterpb.MediaPlacementInventory, *PlacementObservationRequest)
	}{
		{"tenant", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.TenantId = "other"
		}},
		{"cell", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.ControlCellId = "other"
		}},
		{"incomplete", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Complete = false
		}},
		{"missing_cluster", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.ClusterIds = i.ClusterIds[:1]
		}},
		{"duplicate_cluster", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.ClusterIds[1] = i.ClusterIds[0]
		}},
		{"foreign_cluster", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes[0].ClusterId = "other"
		}},
		{"moved_node", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes[0].ClusterId = "empty"
		}},
		{"duplicate_member", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes = append(i.Nodes, i.Nodes[0])
		}},
		{"nil_member", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes[0] = nil
		}},
		{"blank_member", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes[0].NodeId = ""
		}},
		{"duplicate_runtime", func(_ *PlacementCell, _ *quartermasterpb.MediaPlacementInventory, r *PlacementObservationRequest) {
			r.Snapshot.Nodes = append(r.Snapshot.Nodes, r.Snapshot.Nodes[0])
		}},
		{"nil_snapshot", func(_ *PlacementCell, _ *quartermasterpb.MediaPlacementInventory, r *PlacementObservationRequest) {
			r.Snapshot = nil
		}},
		{"stale", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, r *PlacementObservationRequest) {
			i.ObservedAt = timestamppb.New(r.Now.Add(-30 * time.Second))
		}},
		{"future", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, r *PlacementObservationRequest) {
			i.ObservedAt = timestamppb.New(r.Now.Add(time.Nanosecond))
		}},
		{"missing_stamp", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.ObservedAt = nil
		}},
		{"invalid_stamp", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.ObservedAt = &timestamppb.Timestamp{Seconds: 1 << 62}
		}},
		{"duplicate_requested", func(c *PlacementCell, _ *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			c.ClusterIDs[1] = c.ClusterIDs[0]
		}},
		{"oversize", func(_ *PlacementCell, i *quartermasterpb.MediaPlacementInventory, _ *PlacementObservationRequest) {
			i.Nodes = make([]*quartermasterpb.MediaPlacementInventoryNode, 4097)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cell, inventory, req := inventoryFixture()
			tc.mutate(&cell, inventory, &req)
			if got, err := ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now); !errors.Is(err, ErrPlacementInventory) || got != nil {
				t.Fatalf("ambiguous inventory accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestReconcilePlacementInventoryEmptyPool(t *testing.T) {
	cell, inventory, req := inventoryFixture()
	inventory.Nodes = nil
	req.Snapshot = &state.BalancerSnapshot{}
	joined, err := ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now)
	if err != nil || !joined.Complete || len(joined.Snapshot.Nodes) != 0 {
		t.Fatalf("authoritatively empty pool rejected: %+v, %v", joined, err)
	}
}

func TestInventoryObservationBindsExpiryTenantAndCompletePool(t *testing.T) {
	cell, inventory, req := inventoryFixture()
	req.Clusters["empty"] = PlacementClusterFacts{AllowedVerbs: []placement.Verb{placement.Serve}}
	joined, err := ReconcilePlacementInventory(req.TenantID, cell, inventory, req.Snapshot, req.Now)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := joined.Observe(req)
	if err != nil || !observation.Complete || len(observation.Candidates) != 2 {
		t.Fatalf("observe inventory: %+v, %v", observation, err)
	}
	for _, candidate := range observation.Candidates {
		if candidate.ExpiresAt.After(joined.ExpiresAt) {
			t.Fatal("fresh metrics extended membership validity")
		}
		if candidate.NodeID == "node" && !candidate.ExpiresAt.Equal(joined.ExpiresAt) {
			t.Fatal("healthy member did not use the earliest expiry")
		}
	}
	joined.Complete = false
	observation, err = joined.Observe(req)
	if err != nil || observation.Complete {
		t.Fatalf("partial pool became complete: %+v, %v", observation, err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*PlacementObservationRequest)
	}{
		{"tenant", func(r *PlacementObservationRequest) { r.TenantID = "another" }},
		{"expired", func(r *PlacementObservationRequest) { r.Now = joined.ExpiresAt }},
		{"clock_rewind", func(r *PlacementObservationRequest) { r.Now = inventory.ObservedAt.AsTime().Add(-time.Nanosecond) }},
		{"omitted_pool", func(r *PlacementObservationRequest) {
			r.Clusters = map[string]PlacementClusterFacts{"private": r.Clusters["private"]}
		}},
		{"substituted_pool", func(r *PlacementObservationRequest) {
			r.Clusters = map[string]PlacementClusterFacts{"private": r.Clusters["private"], "other": {}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := req
			tc.mutate(&changed)
			if _, err := joined.Observe(changed); !errors.Is(err, ErrPlacementInventory) {
				t.Fatalf("unbound observation accepted: %v", err)
			}
		})
	}
}
