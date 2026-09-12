package mediaauthority

import (
	"testing"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

type runtimePeerFixture struct {
	addresses map[string]string
	connected map[string]bool
}

func (f runtimePeerFixture) GetPeerAddr(clusterID string) string { return f.addresses[clusterID] }
func (f runtimePeerFixture) IsPeerConnected(clusterID string) bool {
	return f.connected[clusterID]
}

func TestRoutingClusterPeersSeparatesAuthorityFromRuntimeReachability(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		PreferredClusterId: "local-a",
		OfficialClusterId:  "remote-b",
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{ClusterId: "local-a"},
			{ClusterId: "remote-b"},
			{ClusterId: "offline-c"},
		},
	}
	store := &Store{runtimePeers: runtimePeerFixture{
		addresses: map[string]string{"remote-b": "foghorn-b:18019", "offline-c": "foghorn-c:18019"},
		connected: map[string]bool{"remote-b": true},
	}}

	routing := store.RoutingClusterPeers(tenant, "local-a")
	if len(routing) != 2 {
		t.Fatalf("routing peers = %+v, want local and connected remote only", routing)
	}
	byID := map[string]int{}
	for index, peer := range routing {
		byID[peer.GetClusterId()] = index
	}
	local := routing[byID["local-a"]]
	if local.GetHealthStatus() != "healthy" || local.GetRole() != "preferred" {
		t.Fatalf("local runtime overlay = %+v", local)
	}
	remote := routing[byID["remote-b"]]
	if remote.GetHealthStatus() != "healthy" || remote.GetRole() != "official" || remote.GetFoghornGrpcAddr() != "foghorn-b:18019" {
		t.Fatalf("remote runtime overlay = %+v", remote)
	}
	if len(TenantClusterPeers(tenant)) != 3 {
		t.Fatal("runtime filtering mutated the stable authority projection")
	}
}

// A tenant's private virtual cluster is served by this cell's own nodes: it is
// not the cell's cluster id and has no federation address, yet viewers on it
// are local. It must stay in the routing peers, or every USER_NEW on the
// private edge is refused as a cluster not authorized for the tenant.
func TestRoutingClusterPeersKeepsClustersThisCellServes(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		OfficialClusterId: "platform-a",
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{ClusterId: "platform-a"},
			{ClusterId: "private-edge"},
			{ClusterId: "offline-c"},
		},
	}
	store := &Store{
		runtimePeers:  runtimePeerFixture{addresses: map[string]string{"platform-a": "foghorn-a:18019"}, connected: map[string]bool{"platform-a": true}},
		servedCluster: func(id string) bool { return id == "private-edge" },
	}

	routing := store.RoutingClusterPeers(tenant, "cell-b")
	byID := map[string]*clusterpeerpb.TenantClusterPeer{}
	for _, peer := range routing {
		byID[peer.GetClusterId()] = peer
	}
	served, ok := byID["private-edge"]
	if !ok || served.GetHealthStatus() != "healthy" || served.GetFoghornGrpcAddr() != "" {
		t.Fatalf("served virtual cluster must be a healthy local peer, got %+v (all: %+v)", served, routing)
	}
	if _, present := byID["offline-c"]; present {
		t.Fatal("an unserved, unconnected cluster must still be filtered")
	}
	without := (&Store{}).RoutingClusterPeers(tenant, "cell-b")
	for _, peer := range without {
		if peer.GetClusterId() == "private-edge" {
			t.Fatal("without a served-cluster resolver the virtual cluster is not local")
		}
	}
}
