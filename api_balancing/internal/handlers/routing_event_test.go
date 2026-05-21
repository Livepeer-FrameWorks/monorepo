package handlers

import "testing"

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
