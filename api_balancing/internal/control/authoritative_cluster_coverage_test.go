package control

import (
	"sync"
	"testing"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

// AuthoritativeClusterServable is the cross-cluster gate for artifact playback,
// and its two "yes" branches are easy to conflate, so this pins them alongside
// the empty/peer cases. The artifact's authoritative byte-cluster is serveable
// when it is THIS foghorn's local cluster or any additional cluster this
// foghorn serves (multi-cluster foghorn), when the resolved envelope identifies
// the former as platform-shared and the latter as an authorized tenant peer.
func TestAuthoritativeClusterServable_LocalAndServedBranches(t *testing.T) {
	prevLocal := localClusterID
	prevServed := servedClusters.Load()
	prevShared := platformSharedConfig.Load()
	t.Cleanup(func() {
		localClusterID = prevLocal
		servedClusters.Store(prevServed)
		platformSharedConfig.Store(prevShared)
	})
	servedClusters.Store(&sync.Map{})
	platformSharedConfig.Store(&sync.Map{})
	SetLocalClusterID("media-central-primary")
	AddPlatformSharedCluster("media-central-primary")
	AddServedCluster("media-edge-secondary")

	if !AuthoritativeClusterServable("media-central-primary", "tenant-a", nil) {
		t.Fatal("platform-shared local cluster must be serveable")
	}
	servedPeers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "media-edge-secondary"}}
	if !AuthoritativeClusterServable("media-edge-secondary", "tenant-a", servedPeers) {
		t.Fatal("authorized served cluster must be serveable")
	}
	// A cluster that is neither local, served, nor a peer must be refused.
	if AuthoritativeClusterServable("foreign-cluster", "tenant-a", nil) {
		t.Fatal("unserved foreign cluster must be refused")
	}
	// ...but the same foreign cluster becomes serveable once it is an
	// authorized tenant peer.
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "foreign-cluster"}}
	if !AuthoritativeClusterServable("foreign-cluster", "tenant-a", peers) {
		t.Fatal("foreign cluster authorized as a tenant peer must be serveable")
	}
}
