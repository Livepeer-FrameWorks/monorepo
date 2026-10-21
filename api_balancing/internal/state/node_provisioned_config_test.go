package state

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A lifecycle report's edge config marker lands on the node state; a report
// without one (the restart announcement) keeps the recorded marker.
func TestApplyNodeLifecycleRecordsProvisionedConfig(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()
	ctx := context.Background()

	if err := sm.ApplyNodeLifecycle(ctx, &ipcpb.NodeLifecycleUpdate{
		NodeId:            "edge-1",
		IsHealthy:         true,
		ProvisionedConfig: &ipcpb.EdgeProvisionedConfig{CliVersion: "v0.3.11", Digest: "sha256:abc", RenderDigest: "sha256:def"},
	}); err != nil {
		t.Fatalf("ApplyNodeLifecycle: %v", err)
	}
	node := sm.GetNodeState("edge-1")
	if node.ProvisionedConfigCLIVersion != "v0.3.11" || node.ProvisionedConfigDigest != "sha256:abc" || node.ProvisionedConfigRenderDigest != "sha256:def" {
		t.Fatalf("recorded marker = %q/%q/%q, want v0.3.11/sha256:abc/sha256:def", node.ProvisionedConfigCLIVersion, node.ProvisionedConfigDigest, node.ProvisionedConfigRenderDigest)
	}

	if err := sm.ApplyNodeLifecycle(ctx, &ipcpb.NodeLifecycleUpdate{NodeId: "edge-1", IsHealthy: true, EventType: "node_restarting"}); err != nil {
		t.Fatalf("ApplyNodeLifecycle: %v", err)
	}
	if node := sm.GetNodeState("edge-1"); node.ProvisionedConfigCLIVersion != "v0.3.11" {
		t.Fatalf("marker-less report cleared the recorded marker: %q", node.ProvisionedConfigCLIVersion)
	}
}
