package resolvers

import (
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"testing"
)

func floatPtr(v float64) *float64 { return &v }

func TestComputeClusterGeoFiltersClusters(t *testing.T) {
	nodes := []*quartermasterpb.InfrastructureNode{
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
	pairs := []*periscopepb.ClusterPairTraffic{
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

func TestAddAssignedPoolClusterGeoUsesPhysicalNodeGeoForVirtualCluster(t *testing.T) {
	nodesByCluster := map[string]*networkClusterGeo{
		"core": {
			nodeCount: 2,
			latSum:    20,
			lonSum:    40,
			geoCount:  2,
		},
	}
	nodesByID := map[string]*quartermasterpb.InfrastructureNode{
		"node-a": {NodeId: "node-a", ClusterId: "core", Latitude: floatPtr(10), Longitude: floatPtr(20), Region: stringPtr("eu")},
		"node-b": {NodeId: "node-b", ClusterId: "core", Latitude: floatPtr(30), Longitude: floatPtr(60), Region: stringPtr("eu")},
	}
	instancesByID := map[string]*quartermasterpb.ServiceInstance{
		"inst-a": {Id: "inst-a", NodeId: stringPtr("node-a")},
		"inst-b": {Id: "inst-b", NodeId: stringPtr("node-b")},
	}

	addAssignedPoolClusterGeo(nodesByCluster, nodesByID, instancesByID, []*quartermasterpb.GetServicePoolStatusResponse{{
		Assignments: []*quartermasterpb.ServiceInstanceAssignment{
			{InstanceId: "inst-a", ClusterId: "media"},
			{InstanceId: "inst-b", ClusterId: "media"},
		},
	}})

	media := nodesByCluster["media"]
	if media == nil {
		t.Fatal("expected virtual media cluster geo")
	}
	if media.geoCount != 2 {
		t.Fatalf("expected two backing nodes, got %d", media.geoCount)
	}
	if got := media.latSum / float64(media.geoCount); got != 20 {
		t.Fatalf("expected averaged latitude 20, got %v", got)
	}
	if got := media.lonSum / float64(media.geoCount); got != 40 {
		t.Fatalf("expected averaged longitude 40, got %v", got)
	}
}

func TestAddAssignedPoolClusterGeoDoesNotOverrideDirectClusterGeo(t *testing.T) {
	nodesByCluster := map[string]*networkClusterGeo{
		"media": {
			latSum:   1,
			lonSum:   2,
			geoCount: 1,
		},
	}
	nodesByID := map[string]*quartermasterpb.InfrastructureNode{
		"node-a": {NodeId: "node-a", Latitude: floatPtr(30), Longitude: floatPtr(60)},
	}
	instancesByID := map[string]*quartermasterpb.ServiceInstance{
		"inst-a": {Id: "inst-a", NodeId: stringPtr("node-a")},
	}

	addAssignedPoolClusterGeo(nodesByCluster, nodesByID, instancesByID, []*quartermasterpb.GetServicePoolStatusResponse{{
		Assignments: []*quartermasterpb.ServiceInstanceAssignment{{InstanceId: "inst-a", ClusterId: "media"}},
	}})

	media := nodesByCluster["media"]
	if media.geoCount != 1 || media.latSum != 1 || media.lonSum != 2 {
		t.Fatalf("expected direct geo to remain unchanged, got %+v", media)
	}
}

func TestAddAssignedPoolClusterGeoDeduplicatesNodeAcrossPoolServices(t *testing.T) {
	nodesByCluster := map[string]*networkClusterGeo{}
	nodesByID := map[string]*quartermasterpb.InfrastructureNode{
		"node-a": {NodeId: "node-a", Latitude: floatPtr(30), Longitude: floatPtr(60)},
	}
	instancesByID := map[string]*quartermasterpb.ServiceInstance{
		"foghorn-a":  {Id: "foghorn-a", NodeId: stringPtr("node-a")},
		"chandler-a": {Id: "chandler-a", NodeId: stringPtr("node-a")},
	}

	addAssignedPoolClusterGeo(nodesByCluster, nodesByID, instancesByID, []*quartermasterpb.GetServicePoolStatusResponse{
		{Assignments: []*quartermasterpb.ServiceInstanceAssignment{{InstanceId: "foghorn-a", ClusterId: "media"}}},
		{Assignments: []*quartermasterpb.ServiceInstanceAssignment{{InstanceId: "chandler-a", ClusterId: "media"}}},
	})

	media := nodesByCluster["media"]
	if media == nil {
		t.Fatal("expected media geo")
	}
	if media.geoCount != 1 || media.latSum != 30 || media.lonSum != 60 {
		t.Fatalf("expected one backing node contribution, got %+v", media)
	}
}
