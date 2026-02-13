package resolvers

import (
	"testing"

	pb "frameworks/pkg/proto"
)

func floatPtr(v float64) *float64 { return &v }

func TestComputeClusterGeoFiltersClusters(t *testing.T) {
	nodes := []*pb.InfrastructureNode{
		{ClusterId: "cluster-a", Latitude: floatPtr(10), Longitude: floatPtr(20)},
		{ClusterId: "cluster-a", Latitude: floatPtr(14), Longitude: floatPtr(24)},
		{ClusterId: "cluster-b", Latitude: floatPtr(30), Longitude: floatPtr(40)},
		{ClusterId: "cluster-c", Latitude: floatPtr(50), Longitude: floatPtr(60)},
	}

	geo := computeClusterGeo(nodes, map[string]struct{}{"cluster-a": {}, "cluster-b": {}})
	if len(geo) != 2 {
		t.Fatalf("expected 2 cluster geos, got %d", len(geo))
	}

	a := geo["cluster-a"]
	if a.lat != 12 || a.lon != 22 {
		t.Fatalf("unexpected cluster-a avg: %+v", a)
	}
	if _, ok := geo["cluster-c"]; ok {
		t.Fatal("cluster-c should be filtered out")
	}
}

func TestApplyTrafficGeoOnlySetsKnownClusters(t *testing.T) {
	pairs := []*pb.ClusterPairTraffic{
		{ClusterId: "cluster-a", RemoteClusterId: "cluster-b"},
		{ClusterId: "cluster-a", RemoteClusterId: "cluster-z"},
	}

	applyTrafficGeo(pairs, map[string]struct{ lat, lon float64 }{
		"cluster-a": {lat: 1, lon: 2},
		"cluster-b": {lat: 3, lon: 4},
	})

	if pairs[0].LocalLatitude == nil || *pairs[0].LocalLatitude != 1 {
		t.Fatalf("expected local latitude on first pair, got %+v", pairs[0].LocalLatitude)
	}
	if pairs[0].RemoteLatitude == nil || *pairs[0].RemoteLatitude != 3 {
		t.Fatalf("expected remote latitude on first pair, got %+v", pairs[0].RemoteLatitude)
	}
	if pairs[1].RemoteLatitude != nil || pairs[1].RemoteLongitude != nil {
		t.Fatal("expected unknown remote cluster geo to remain unset")
	}
}
