package grpc

import (
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"testing"
)

func TestStreamOriginRegionForRouteUsesActiveClusterRegion(t *testing.T) {
	route := &clusterRoute{
		clusterID: "media-eu-1",
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "media-eu-1", RegionId: "eu-west"},
			{ClusterId: "media-us-1", RegionId: "us-east"},
		},
	}
	if got := streamOriginRegionForRoute(route, "media-us-1"); got != "us-east" {
		t.Fatalf("expected active cluster region us-east, got %q", got)
	}
}

func TestStreamOriginRegionForRouteFallsBackToPreferredCluster(t *testing.T) {
	route := &clusterRoute{
		clusterID: "media-eu-1",
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{
			{ClusterId: "media-eu-1", RegionId: "eu-west"},
			{ClusterId: "media-us-1", RegionId: "us-east"},
		},
	}
	if got := streamOriginRegionForRoute(route, ""); got != "eu-west" {
		t.Fatalf("expected preferred cluster region eu-west, got %q", got)
	}
}
