package coordinator

import (
	"testing"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

// Gateway telemetry carries no gateway position, so a gateway observer is
// placed at its Quartermaster node, or at the node's cluster centroid, and is
// otherwise unknown rather than placed from a region label.
func TestLocateGatewaysUsesNodeThenClusterCentroid(t *testing.T) {
	s := store.NewState()
	for _, id := range []string{"gateway:regional-eu-1", "gateway:regional-eu-2", "gateway:stg-us-1", "gateway:unlisted"} {
		s.Observers[id] = obs.Observer{ID: id, Kind: "gateway", Region: "eu-west"}
	}
	configured := obs.Location{Known: true, Latitude: 1, Longitude: 2, Source: "configured"}
	s.Observers["gateway:configured"] = obs.Observer{ID: "gateway:configured", Kind: "gateway", Location: configured}
	prober := obs.Location{Known: true, Latitude: 40, Longitude: -74, Source: "mmdb"}
	s.Observers["prober-1"] = obs.Observer{ID: "prober-1", Kind: "prober", Location: prober}

	LocateGateways(s, []NodeLocation{
		{NodeID: "regional-eu-1", ClusterID: "regional-eu", Latitude: 52, Longitude: 4, Known: true},
		{NodeID: "regional-eu-2", ClusterID: "regional-eu"},
		{NodeID: "regional-eu-3", ClusterID: "regional-eu", Latitude: 50, Longitude: 8, Known: true},
		{NodeID: "stg-us-1", ClusterID: "staging-regional-us"},
		{NodeID: "configured", ClusterID: "regional-us", Latitude: 40, Longitude: -74, Known: true},
	})

	if got := s.Observers["gateway:regional-eu-1"].Location; !got.Valid() || got.Latitude != 52 || got.Longitude != 4 || got.Source != GatewayLocationNode {
		t.Fatalf("located node gateway = %+v, want its node position", got)
	}
	if got := s.Observers["gateway:regional-eu-2"].Location; !got.Valid() || got.Latitude != 51 || got.Longitude != 6 || got.Source != GatewayLocationCluster {
		t.Fatalf("unlocated node gateway = %+v, want the cluster centroid 51,6", got)
	}
	if got := s.Observers["gateway:stg-us-1"].Location; got.Known {
		t.Fatalf("gateway in a cluster without positions = %+v, want unknown", got)
	}
	if got := s.Observers["gateway:unlisted"].Location; got.Known || got.Source != "" {
		t.Fatalf("gateway missing from the node list = %+v, want it untouched", got)
	}
	if got := s.Observers["gateway:configured"].Location; got != configured {
		t.Fatalf("configured gateway location = %+v, want %+v kept", got, configured)
	}
	if got := s.Observers["prober-1"].Location; got != prober {
		t.Fatalf("prober location = %+v, want %+v kept", got, prober)
	}

	// A node that loses its position falls back to the cluster again.
	LocateGateways(s, []NodeLocation{
		{NodeID: "regional-eu-1", ClusterID: "regional-eu"},
		{NodeID: "regional-eu-3", ClusterID: "regional-eu", Latitude: 50, Longitude: 8, Known: true},
	})
	if got := s.Observers["gateway:regional-eu-1"].Location; got.Source != GatewayLocationCluster || got.Latitude != 50 {
		t.Fatalf("refreshed gateway = %+v, want the remaining cluster centroid", got)
	}
}
