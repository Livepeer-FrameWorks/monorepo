package federation

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

func TestQueryStreamGenerationDescribesExactPublisherNotLastReportingNode(t *testing.T) {
	server, _, _ := testFederationServerWithCache(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	for _, nodeID := range []string{"publisher", "relay"} {
		sm.SetNodeInfo(nodeID, "https://"+nodeID+".example.com", true, nil, nil, "", "", map[string]any{"DTSC": "dtsc://HOST/$"})
		sm.SetNodeConnectionInfo(context.Background(), nodeID, "", "tenant-a", "virtual-source", nil)
		sm.UpdateNodeMetrics(nodeID, struct {
			CPU, RAMMax, RAMCurrent, UpSpeed, DownSpeed, BWLimit float64
			CapIngest, CapEdge, CapStorage, CapProcessing        bool
			Roles                                                []string
			StorageCapacityBytes, StorageUsedBytes               uint64
			ProcessingClasses                                    map[string]state.ClassCapacity
		}{CPU: 10, RAMMax: 1024, RAMCurrent: 128, BWLimit: 1000000, CapIngest: true, CapEdge: true})
		sm.TouchNode(nodeID, true)
		sm.SetProbeVerified(nodeID, true)
		if err := sm.UpdateStreamFromBuffer("stream", "stream", nodeID, "tenant-a", "FULL", ""); err != nil {
			t.Fatal(err)
		}
		sm.UpdateNodeStats("stream", nodeID, 1, 1, 0, 0, nodeID == "relay")
	}
	registry := control.StreamRegistryInstance
	registry.UpsertLocalSource(control.StreamEntry{InternalName: "stream", TenantID: "tenant-a", IngestMode: control.IngestPush})
	if _, applied, err := registry.ProjectSource("stream", "publisher", 1, "trigger", "generation", 7); err != nil || !applied {
		t.Fatalf("project publisher: %v %v", applied, err)
	}
	server.lb = balancer.NewLoadBalancer(logging.NewLogger())
	server.lb.SetClusterAccessAuthorizer(func(clusterID, tenantID string) bool { return clusterID == "virtual-source" && tenantID == "tenant-a" })
	response, err := server.QueryStream(svcAuthCtx(), &federationpb.QueryStreamRequest{StreamName: "stream", TenantId: "tenant-a", RequestingCluster: "cluster-b"})
	if err != nil || len(response.GetCandidates()) != 2 {
		t.Fatalf("query candidates: %v %v", response, err)
	}
	for _, candidate := range response.Candidates {
		if candidate.GetClusterId() != "virtual-source" {
			t.Fatalf("source node borrowed responding cell identity: %v", candidate)
		}
		if candidate.GetBufferState() != "FULL" {
			t.Fatalf("node-specific buffer state lost: %v", candidate)
		}
		if candidate.GetNodeId() == "publisher" {
			if candidate.GetSourceGeneration() != "generation" || candidate.GetSourceRevision() != 7 || !candidate.GetIsOrigin() {
				t.Fatalf("publisher identity lost to last-reporting relay: %v", candidate)
			}
		} else if candidate.GetSourceGeneration() != "" || candidate.GetSourceRevision() != 0 || candidate.GetIsOrigin() {
			t.Fatalf("relay advertised as confirmed publisher: %v", candidate)
		}
	}
}
