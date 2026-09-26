package triggers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

// Staging: a peer cluster without a running Foghorn (no address from
// Quartermaster) was recorded in the admission's peer set, and the reader then
// poisoned the whole broadcast leg, so no peer cell learned the stream was
// live. Such a peer is skipped; the complete peers are kept.
func TestAdmissionPeerHintsSkipPeersWithoutFoghornAddress(t *testing.T) {
	p := &Processor{logger: logging.NewLogger(), clusterID: "media-eu"}
	hints := p.admissionPeerHints("live+s", []*clusterpeerpb.TenantClusterPeer{
		{ClusterId: "media-eu", FoghornGrpcAddr: "foghorn-eu:18019"},
		{ClusterId: "media-us", FoghornGrpcAddr: " foghorn-us:18019 ", Role: "official"},
		{ClusterId: "staging-core", FoghornGrpcAddr: ""},
		nil,
	})
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want only media-us", hints)
	}
	if hints[0].ClusterID != "media-us" || hints[0].Addr != "foghorn-us:18019" || !hints[0].AlwaysOn {
		t.Fatalf("hint = %+v", hints[0])
	}
}
