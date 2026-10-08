package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListNodeUpdateStatusReportsPhaseErrorAndVersions(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	sm := seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")
	sm.SetNodeRuntimeInfo("node-1", "native", "linux", "amd64", "cpu")

	deadline := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM foghorn\.node_update_state`).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "target_release", "phase", "last_error", "deadline", "updated_at"}).
			AddRow("node-1", "stable:v0.3.11", "failed", "helmsman did not reconnect after self-update", deadline, deadline))
	mock.ExpectQuery(`FROM foghorn\.node_components`).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "component", "current_version"}).
			AddRow("node-1", "helmsman", "v0.3.10").AddRow("node-1", "mist", "v0.3.10"))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := srv.ListNodeUpdateStatus(ctx, &foghorncontrolpb.ListNodeUpdateStatusRequest{ClusterId: "cluster-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("nodes = %d, want 1", len(resp.GetNodes()))
	}
	node := resp.GetNodes()[0]
	if node.GetPhase() != "failed" || node.GetLastError() != "helmsman did not reconnect after self-update" || node.GetTargetRelease() != "stable:v0.3.11" {
		t.Fatalf("update state not surfaced: %+v", node)
	}
	if node.GetPhaseDeadline() != "2026-10-08T12:00:00Z" || !node.GetConnected() || !node.GetAutomaticUpdates() || node.GetOperationalMode() != "normal" {
		t.Fatalf("node fields wrong: %+v", node)
	}
	if len(node.GetComponentVersions()) != 2 {
		t.Fatalf("component versions = %+v", node.GetComponentVersions())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A tenant caller sees only the nodes it owns, as node lifecycle reads do.
func TestListNodeUpdateStatusHidesOtherTenantsNodes(t *testing.T) {
	srv, _ := newLifecycleServer(t)
	seedHealthyNode(t, "node-1", "tenant-owner", "cluster-1")

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-intruder")
	resp, err := srv.ListNodeUpdateStatus(ctx, &foghorncontrolpb.ListNodeUpdateStatusRequest{ClusterId: "cluster-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetNodes()) != 0 {
		t.Fatalf("foreign tenant saw nodes: %+v", resp.GetNodes())
	}

	if _, err := srv.ListNodeUpdateStatus(context.Background(), &foghorncontrolpb.ListNodeUpdateStatusRequest{ClusterId: "cluster-1"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated call: %v", err)
	}
	if _, err := srv.ListNodeUpdateStatus(ctx, &foghorncontrolpb.ListNodeUpdateStatusRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing cluster_id: %v", err)
	}
}
