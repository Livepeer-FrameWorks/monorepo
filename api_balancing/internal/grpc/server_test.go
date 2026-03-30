package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockCacheInvalidator struct {
	lastTenant string
	entries    int
}

func (m *mockCacheInvalidator) InvalidateTenantCache(tenantID string) int {
	m.lastTenant = tenantID
	return m.entries
}

func (m *mockCacheInvalidator) GetBillingStatus(ctx context.Context, internalName, tenantID string) *triggers.BillingStatus {
	return nil
}

func (m *mockCacheInvalidator) GetClusterPeers(internalName, tenantID string) []*pb.TenantClusterPeer {
	return nil
}

func TestInvalidateTenantCacheRequiresTenantID(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	_, err := server.InvalidateTenantCache(context.Background(), &pb.InvalidateTenantCacheRequest{})
	if err == nil {
		t.Fatal("expected error for missing tenant id")
	}

	statusErr, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected grpc status error")
	}
	if statusErr.Code() != codes.InvalidArgument {
		t.Fatalf("expected invalid argument error, got %s", statusErr.Code())
	}
}

func TestInvalidateTenantCacheNoInvalidatorConfigured(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	resp, err := server.InvalidateTenantCache(context.Background(), &pb.InvalidateTenantCacheRequest{
		TenantId: "tenant-1",
		Reason:   "reactivate",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EntriesInvalidated != 0 {
		t.Fatalf("expected 0 invalidated entries, got %d", resp.EntriesInvalidated)
	}
}

func TestInvalidateTenantCacheUsesInvalidator(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	invalidator := &mockCacheInvalidator{entries: 3}
	server.SetCacheInvalidator(invalidator)

	resp, err := server.InvalidateTenantCache(context.Background(), &pb.InvalidateTenantCacheRequest{
		TenantId: "tenant-2",
		Reason:   "reactivate",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.EntriesInvalidated != 3 {
		t.Fatalf("expected 3 invalidated entries, got %d", resp.EntriesInvalidated)
	}
	if invalidator.lastTenant != "tenant-2" {
		t.Fatalf("expected tenant-2 to be invalidated, got %s", invalidator.lastTenant)
	}
}

func TestLookupCompletedUploadAssetReturnsFailedAssetWhenPipelineFailed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT a.id, a.artifact_hash, a.status").
		WithArgs("art-1").
		WillReturnError(context.DeadlineExceeded)

	asset, err := server.lookupCompletedUploadAsset("art-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if asset.GetArtifactHash() != "art-1" {
		t.Fatalf("expected artifact hash art-1, got %s", asset.GetArtifactHash())
	}
	if asset.GetStatus() != pb.VodStatus_VOD_STATUS_FAILED {
		t.Fatalf("expected failed status, got %v", asset.GetStatus())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
