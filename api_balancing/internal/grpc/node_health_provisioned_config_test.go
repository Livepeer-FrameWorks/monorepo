package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// Node health carries the render digest from the node's edge config marker,
// which `cluster doctor` compares with the render digest of its own CLI.
func TestGetNodeHealth_ReportsProvisionedConfigRenderDigest(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	sm := seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")
	sm.SetNodeProvisionedConfig("node-1", &ipcpb.EdgeProvisionedConfig{CliVersion: "v0.3.11", Digest: "sha256:abc", RenderDigest: "sha256:def"})
	mock.ExpectQuery(`SELECT component, COALESCE\(current_version`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"component", "version"}))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := srv.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetProvisionedConfigRenderDigest() != "sha256:def" {
		t.Fatalf("provisioned config render digest = %q, want sha256:def", resp.GetProvisionedConfigRenderDigest())
	}
}
