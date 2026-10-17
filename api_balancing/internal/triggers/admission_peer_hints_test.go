package triggers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
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
		{ClusterId: "media-ap", ClusterType: "edge", FoghornGrpcAddr: ""},
		nil,
	})
	if len(hints) != 1 {
		t.Fatalf("hints = %+v, want only media-us", hints)
	}
	if hints[0].ClusterID != "media-us" || hints[0].Addr != "foghorn-us:18019" || !hints[0].AlwaysOn {
		t.Fatalf("hint = %+v", hints[0])
	}
}

// Staging rc10: every admission warned that staging-core and the regional
// control clusters had no Foghorn address. Those are control-plane clusters
// (cluster_type central) without a cell and are skipped silently; a central
// platform cluster that runs a cell's Foghorn (the two-cell stack's us-primary)
// stays a peer. Only a media cluster that lacks its Foghorn is a
// misconfiguration, reported once per cluster rather than once per stream.
func TestAdmissionPeerHintsIgnoreControlClustersAndReportMissingMediaPeerOnce(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	p := &Processor{logger: logger, clusterID: "staging-media-eu"}
	peers := []*clusterpeerpb.TenantClusterPeer{
		{ClusterId: "staging-media-eu", ClusterType: "edge", FoghornGrpcAddr: "foghorn-eu:18019"},
		{ClusterId: "staging-media-us", ClusterType: "edge", FoghornGrpcAddr: "foghorn-us:18019"},
		{ClusterId: "staging-core", ClusterType: "central"},
		{ClusterId: "staging-regional-eu", ClusterType: "central"},
		{ClusterId: "us-primary", ClusterType: "central", FoghornGrpcAddr: "foghorn-b:18019"},
		{ClusterId: "staging-media-ap", ClusterType: "edge"},
	}
	for _, stream := range []string{"live+a", "live+b", "live+c"} {
		hints := p.admissionPeerHints(stream, peers)
		if len(hints) != 2 || hints[0].ClusterID != "staging-media-us" || hints[1].ClusterID != "us-primary" {
			t.Fatalf("%s: hints = %+v, want staging-media-us and us-primary", stream, hints)
		}
	}
	var warned []string
	for _, e := range hook.AllEntries() {
		if e.Level <= logrus.WarnLevel {
			warned = append(warned, e.Data["peer_cluster"].(string))
		}
	}
	if len(warned) != 1 || warned[0] != "staging-media-ap" {
		t.Fatalf("warnings for peer clusters %v, want one for staging-media-ap", warned)
	}
}
