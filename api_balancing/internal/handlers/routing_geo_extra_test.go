package handlers

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

// BuildLoadBalancingData fills the optional proto fields only when present, and
// computes a non-zero great-circle distance when both endpoints have valid,
// distinct coordinates. This complements the existing zero-distance / no-coords
// tests by pinning the populated path: distance > 0, latency/candidates wrapped,
// and the optional string fields set.
func TestBuildLoadBalancingData_PopulatedOptionalFields(t *testing.T) {
	state.ResetDefaultManagerForTests()
	e := &RoutingEvent{
		Status:    "success",
		ClientLat: 52.0, ClientLon: 5.0,
		NodeLat: 48.0, NodeLon: 2.0, // distinct from client → non-zero distance
		LatencyMs:       12.5,
		CandidatesCount: 3,
		EventType:       "load_balancing",
		Source:          "http",
		RemoteClusterID: "cluster-remote",
		InternalName:    "live+x",
	}
	data := BuildLoadBalancingData(e)

	if data.RoutingDistanceKm == nil || *data.RoutingDistanceKm <= 0 {
		t.Fatalf("expected positive routing distance, got %v", data.RoutingDistanceKm)
	}
	if data.LatencyMs == nil || *data.LatencyMs != 12.5 {
		t.Errorf("latency = %v, want 12.5", data.LatencyMs)
	}
	if data.CandidatesCount == nil || *data.CandidatesCount != 3 {
		t.Errorf("candidates = %v, want 3", data.CandidatesCount)
	}
	if data.EventType == nil || *data.EventType != "load_balancing" {
		t.Errorf("event_type = %v", data.EventType)
	}
	if data.RemoteClusterId == nil || *data.RemoteClusterId != "cluster-remote" {
		t.Errorf("remote_cluster_id = %v", data.RemoteClusterId)
	}
}

// EnrichRoutingEventNodeFromState backfills the node name from state when the
// event omitted it: a node with no Location falls back to its NodeID.
func TestEnrichRoutingEventNodeFromState_NameFallsBackToNodeID(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 40.0, -74.0
	// SetNodeInfo with empty location → NodeName must fall back to NodeID.
	sm.SetNodeInfo("edge-7", "edge-7.example.com", true, &lat, &lon, "", "", nil)

	e := &RoutingEvent{SelectedNodeID: "edge-7"}
	EnrichRoutingEventNodeFromState(e)
	if e.NodeName != "edge-7" {
		t.Fatalf("NodeName = %q, want edge-7 (NodeID fallback)", e.NodeName)
	}
	if e.NodeLat != 40.0 || e.NodeLon != -74.0 {
		t.Fatalf("node coords not backfilled: (%v,%v)", e.NodeLat, e.NodeLon)
	}
}

// SetSelfGeo / GetSelfGeo are a simple package-global round-trip used to stamp
// this Foghorn's own location onto routing events.
func TestSelfGeoRoundTrip(t *testing.T) {
	prevLat, prevLon, prevLoc := GetSelfGeo()
	t.Cleanup(func() { SetSelfGeo(prevLat, prevLon, prevLoc) })

	SetSelfGeo(12.5, -34.0, "ams")
	lat, lon, loc := GetSelfGeo()
	if lat != 12.5 || lon != -34.0 || loc != "ams" {
		t.Fatalf("GetSelfGeo = (%v,%v,%q), want (12.5,-34,ams)", lat, lon, loc)
	}
}
