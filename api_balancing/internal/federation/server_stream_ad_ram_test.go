package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"

	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// A StreamAdvertisement's per-edge RAM fields must survive into the registry's
// EdgeCandidates — remote-edge scoring rejects RAMMax==0, so dropping them
// here would silently disable the ad-fed pre-warm path on the /play side.
func TestHandleStreamAdvertisement_MapsRAMIntoEdgeCandidates(t *testing.T) {
	cache, _ := setupTestCache(t)
	srv := NewFederationServer(FederationServerConfig{
		Logger:    testLogger(),
		ClusterID: "cluster-a",
		Cache:     cache,
	})

	prev := control.StreamRegistryInstance
	control.StreamRegistryInstance = control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	t.Cleanup(func() { control.StreamRegistryInstance = prev })

	srv.handleStreamAdvertisement(context.Background(), "cluster-b", &foghornfederationpb.StreamAdvertisement{
		InternalName: "s1",
		TenantId:     "tenant-1",
		IsLive:       true,
		Timestamp:    time.Now().Unix(),
		Edges: []*foghornfederationpb.PeerStreamEdge{
			{NodeId: "edge-1", BaseUrl: "https://e1", DtscUrl: "dtsc://e1/live+s1", BwAvailable: 1000, RamUsed: 256, RamMax: 1024},
		},
	})

	got := control.StreamRegistryInstance.FederatedEdgeCandidates("s1", 20*time.Second)
	if len(got["cluster-b"]) != 1 {
		t.Fatalf("candidates = %+v, want one cluster-b edge", got)
	}
	c := got["cluster-b"][0]
	if c.RAMUsed != 256 || c.RAMMax != 1024 {
		t.Fatalf("RAM fields lost in ad mapping: %+v", c)
	}
	if c.NodeID != "edge-1" || c.DTSCURL != "dtsc://e1/live+s1" {
		t.Fatalf("edge fields wrong: %+v", c)
	}
}
