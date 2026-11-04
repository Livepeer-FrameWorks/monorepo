package grpc

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestOperatorCanDiagnoseAnotherTenantsNodeWithoutMutationAccess(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	seedHealthyNode(t, "operator-diagnosis-node", "tenant-owner", "cluster-one")
	mock.ExpectQuery(`SELECT component, COALESCE\(current_version`).WithArgs("operator-diagnosis-node").WillReturnRows(sqlmock.NewRows([]string{"component", "version"}))
	ctx := context.WithValue(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "operator"), ctxkeys.KeyTenantID, "operator-account"), ctxkeys.KeyPlatformOperator, true)
	if _, err := srv.GetNodeHealth(ctx, &foghorncontrolpb.GetNodeHealthRequest{NodeId: "operator-diagnosis-node"}); err != nil {
		t.Fatalf("operator support read denied: %v", err)
	}
	if _, err := srv.SetNodeOperationalMode(ctx, &foghorncontrolpb.SetNodeModeRequest{NodeId: "operator-diagnosis-node", Mode: "maintenance"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("diagnostic privilege broadened mutation access: %v", err)
	}
}
