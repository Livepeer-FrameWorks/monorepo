package mediaauthority

import (
	"testing"

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
