package main

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
)

func TestSnapshotToProtoCarriesObservationTime(t *testing.T) {
	observed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	node := snapshotToProto(state.NodeDNSSnapshot{NodeID: "edge-1", IsHealthy: true, ClusterID: "private-eu", ObservedAt: observed})
	if node.GetNodeId() != "edge-1" || node.GetClusterId() != "private-eu" || !node.GetIsHealthy() {
		t.Fatalf("node = %+v", node)
	}
	if !node.GetObservedAt().AsTime().Equal(observed) {
		t.Fatalf("observed_at = %v, want %v", node.GetObservedAt().AsTime(), observed)
	}
	if tombstone := snapshotToProto(state.NodeDNSSnapshot{NodeID: "edge-1"}); tombstone.GetObservedAt() != nil {
		t.Fatalf("a tombstone without an observation carries observed_at %v", tombstone.GetObservedAt())
	}
}
