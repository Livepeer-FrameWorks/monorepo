package triggers

import (
	"testing"

	"frameworks/api_balancing/internal/control"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func TestArtifactSourceAccessSurvivesPeerReconnectButNotRevocation(t *testing.T) {
	granted := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "remote-origin"}}
	response := &commodorepb.ResolveArtifactInternalNameResponse{AuthorityClusterPeers: granted}
	if !control.AuthoritativeClusterServableWithPolicy("remote-origin", "tenant", artifactSourceAuthorizationPeers(response, true), false) {
		t.Fatal("a disconnected origin erased a valid signed artifact source grant")
	}
	response.AuthorityClusterPeers = nil
	response.ClusterPeers = granted
	if control.AuthoritativeClusterServableWithPolicy("remote-origin", "tenant", artifactSourceAuthorizationPeers(response, true), false) {
		t.Fatal("runtime reachability resurrected a revoked signed grant")
	}
	if !control.AuthoritativeClusterServableWithPolicy("remote-origin", "tenant", artifactSourceAuthorizationPeers(response, false), false) {
		t.Fatal("connected evaluator response lost its authority envelope")
	}
	if peers := artifactSourceAuthorizationPeers(nil, true); len(peers) != 0 {
		t.Fatal("missing authority produced grants")
	}
}
