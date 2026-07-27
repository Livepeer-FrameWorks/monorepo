package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// The DVR origin cluster is resolved ONCE and reused for both the Commodore intent and
// Foghorn's artifact row. An explicit cluster_id on the request wins outright.
func TestResolveDVROriginCluster_ExplicitWins(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	srv := &FoghornGRPCServer{clusterID: "local-cell"}
	req := &sharedpb.StartDVRRequest{ClusterId: "cluster-x", InternalName: "live+s1", TenantId: "tenant-a"}

	got, ok := srv.resolveDVROriginCluster(req, "node-a")
	if !ok || got != "cluster-x" {
		t.Fatalf("expected (cluster-x, true), got (%q, %v)", got, ok)
	}
}

// When enrichment yields nothing (no stream identity) the resolver falls back to the storage
// node's cluster before this Foghorn's own cell.
func TestResolveDVROriginCluster_StorageNodeFallback(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	nodeID := "node-a"
	sm.SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "", "tenant-a", "cluster-node", nil)

	srv := &FoghornGRPCServer{clusterID: "local-cell"}
	// Empty InternalName makes enrichClusterID return "" immediately, forcing the fallback.
	req := &sharedpb.StartDVRRequest{InternalName: "", TenantId: "tenant-a"}

	got, ok := srv.resolveDVROriginCluster(req, nodeID)
	if !ok || got != "cluster-node" {
		t.Fatalf("expected (cluster-node, true), got (%q, %v)", got, ok)
	}
}

// With no explicit cluster, no stream identity, and no node state, the resolver falls back to
// the configured Foghorn cell (s.clusterID) — the create still gets a non-empty origin.
func TestResolveDVROriginCluster_CellFallback(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	srv := &FoghornGRPCServer{clusterID: "local-cell"}
	req := &sharedpb.StartDVRRequest{InternalName: "", TenantId: "tenant-a"}

	got, ok := srv.resolveDVROriginCluster(req, "unknown-node")
	if !ok || got != "local-cell" {
		t.Fatalf("expected (local-cell, true), got (%q, %v)", got, ok)
	}
}

// When every source is empty the resolver reports ok=false so startDVR fails the create
// instead of persisting an unconvergeable empty-cluster intent.
func TestResolveDVROriginCluster_AllEmptyFailsClosed(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	srv := &FoghornGRPCServer{clusterID: ""}
	req := &sharedpb.StartDVRRequest{InternalName: "", TenantId: "tenant-a"}

	got, ok := srv.resolveDVROriginCluster(req, "unknown-node")
	if ok || got != "" {
		t.Fatalf("expected (\"\", false), got (%q, %v)", got, ok)
	}
}
