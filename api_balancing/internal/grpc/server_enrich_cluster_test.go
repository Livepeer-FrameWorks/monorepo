package grpc

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestEnrichClusterID_RespectsTenantOnFallback(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	nodeID := "node-a"
	streamName := "shared-stream"

	sm.SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(nodeID, "", "tenant-a", "cluster-a", nil)
	if err := sm.UpdateStreamFromBuffer(streamName, streamName, nodeID, "tenant-a", "ready", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	server := &FoghornGRPCServer{}

	if got := server.enrichClusterID("", streamName, "tenant-a"); got != "cluster-a" {
		t.Fatalf("expected cluster-a for matching tenant, got %q", got)
	}
	if got := server.enrichClusterID("", streamName, "tenant-b"); got != "" {
		t.Fatalf("expected empty cluster for mismatched tenant, got %q", got)
	}
	if got := server.enrichClusterID("cluster-explicit", streamName, "tenant-b"); got != "cluster-explicit" {
		t.Fatalf("expected explicit cluster to win, got %q", got)
	}
}
