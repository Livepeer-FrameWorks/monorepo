package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestPushStreamAdsUsesExactNodeBufferPublisherAndVirtualCluster(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	previous := control.StreamRegistryInstance
	registry := control.NewStreamRegistry(nil, "source-cell", time.Minute)
	control.StreamRegistryInstance = registry
	t.Cleanup(func() { control.StreamRegistryInstance = previous })
	for _, node := range []string{"publisher", "replica"} {
		seedFederationNodeAndStream(t, sm, node, "stream", "tenant")
		sm.SetNodeConnectionInfo(context.Background(), node, "", "tenant", "virtual-source", nil)
		sm.SetNodeInfo(node, "https://"+node+".example", true, nil, nil, "", "", map[string]any{"DTSC": "dtsc://HOST:14200/$"})
		sm.UpdateNodeStats("stream", node, 1, 1, 0, 0, node == "replica")
	}
	if err := sm.UpdateStreamFromBuffer("stream", "stream", "replica", "tenant", "DRY", ""); err != nil {
		t.Fatal(err)
	}
	if err := sm.UpdateStreamFromBuffer("stream", "stream", "publisher", "tenant", "FULL", ""); err != nil {
		t.Fatal(err)
	}
	registry.UpsertLocalSource(control.StreamEntry{InternalName: "stream", TenantID: "tenant", IngestMode: control.IngestPush})
	const revision int64 = 9007199254740993
	if _, applied, err := registry.ProjectSource("stream", "publisher", 1, "trigger", "generation", revision); err != nil || !applied {
		t.Fatalf("project source: %v %v", applied, err)
	}
	pm := newTestPeerManager(t, "source-cell", nil, false)
	out := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer"] = &peerState{connected: true, stream: out, lifecycle: peerAlwaysOn, tenantIDs: []string{"tenant"}}
	pm.mu.Unlock()
	flush := pm.wireTestWriters()
	pm.pushStreamAds()
	flush()
	if len(out.sent) != 1 || len(out.sent[0].GetStreamAd().GetEdges()) != 2 {
		t.Fatalf("unexpected stream advertisements: %+v", out.sent)
	}
	edges := make(map[string]*federationpb.PeerStreamEdge)
	for _, edge := range out.sent[0].GetStreamAd().GetEdges() {
		edges[edge.NodeId] = edge
		if edge.ClusterId != "virtual-source" || edge.RamUsed != 256 || edge.RamMax != 2048 {
			t.Fatalf("wrong node facts: %+v", edge)
		}
	}
	publisher, replica := edges["publisher"], edges["replica"]
	if publisher.GetDtscUrl() != "dtsc://publisher.example:14200/live+stream" || publisher.GetDtscObservedAt() == 0 || publisher.GetSourceObservedAt() == 0 {
		t.Fatalf("publisher lost independent listener/buffer evidence: %+v", publisher)
	}
	if publisher.GetBufferState() != "FULL" || !publisher.GetPlayable() || !publisher.GetIsOrigin() || publisher.GetSourceGeneration() != "generation" || publisher.GetSourceRevision() != revision {
		t.Fatalf("publisher evidence lost: %+v", publisher)
	}
	if replica.GetBufferState() != "DRY" || !replica.GetPlayable() || replica.GetIsOrigin() || replica.GetSourceGeneration() != "" || replica.GetSourceRevision() != 0 {
		t.Fatalf("replica borrowed publisher evidence: %+v", replica)
	}
	if inactive, err := registry.PublishSourceInactive("stream", "publisher", "generation", revision+1); err != nil || !inactive {
		t.Fatalf("withdraw source: %v %v", inactive, err)
	}
	pm.pushStreamAds()
	flush()
	if len(out.sent) != 2 {
		t.Fatalf("missing post-withdrawal observation: %+v", out.sent)
	}
	for _, edge := range out.sent[1].GetStreamAd().GetEdges() {
		if edge.GetSourceGeneration() != "" || edge.GetSourceRevision() != 0 {
			t.Fatalf("inactive source retained a publisher binding: %+v", edge)
		}
	}
	sm.SetNodeInfo("publisher", "https://publisher.example", true, nil, nil, "", "{}", nil)
	pm.pushStreamAds()
	flush()
	for _, edge := range out.sent[2].GetStreamAd().GetEdges() {
		if edge.GetNodeId() == "publisher" && (edge.GetDtscUrl() != "" || edge.GetDtscObservedAt() != 0) {
			t.Fatalf("withdrawn listener was fabricated from cached addressing: %+v", edge)
		}
	}
}
