package handlers

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestBuildLoadBalancingDataPreservesZeroDistance(t *testing.T) {
	data := BuildLoadBalancingData(&RoutingEvent{
		ClientLat: 52.2249,
		ClientLon: 6.852,
		NodeLat:   52.2249,
		NodeLon:   6.852,
	})

	if data.RoutingDistanceKm == nil {
		t.Fatal("RoutingDistanceKm is nil, want explicit zero for valid same-location coordinates")
	}
	if *data.RoutingDistanceKm != 0 {
		t.Fatalf("RoutingDistanceKm = %v, want 0", *data.RoutingDistanceKm)
	}
}

func TestBuildLoadBalancingDataLeavesDistanceNilWithoutValidCoordinates(t *testing.T) {
	data := BuildLoadBalancingData(&RoutingEvent{
		ClientLat: 0,
		ClientLon: 0,
		NodeLat:   52.2249,
		NodeLon:   6.852,
	})

	if data.RoutingDistanceKm != nil {
		t.Fatalf("RoutingDistanceKm = %v, want nil without valid client coordinates", *data.RoutingDistanceKm)
	}
}

func TestBuildLoadBalancingDataEnrichesSelectedNodeBucketFromState(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	lat := 52.1636
	lon := 4.4802
	sm.SetNodeInfo("edge-node-1", "https://edge.example/view", true, &lat, &lon, "Leiden", "", nil)

	data := BuildLoadBalancingData(&RoutingEvent{
		ClientLat:      52.30638,
		ClientLon:      4.87357,
		SelectedNodeID: "edge-node-1",
	})

	if data.NodeBucket == nil || data.NodeBucket.H3Index == 0 {
		t.Fatal("expected node bucket from selected node state")
	}
	if data.NodeLatitude == 0 || data.NodeLongitude == 0 {
		t.Fatalf("expected node centroid coordinates, got %v,%v", data.NodeLatitude, data.NodeLongitude)
	}
	if data.NodeName != "Leiden" {
		t.Fatalf("NodeName = %q, want location name", data.NodeName)
	}
	if data.RoutingDistanceKm == nil {
		t.Fatal("expected routing distance after node geo enrichment")
	}
}
