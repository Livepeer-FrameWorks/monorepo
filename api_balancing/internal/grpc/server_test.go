package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

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

func (m *mockCacheInvalidator) InvalidatePlaybackAuthCache(tenantID string, internalNames []string) int {
	m.lastTenant = tenantID
	return m.entries
}

func (m *mockCacheInvalidator) GetBillingStatus(ctx context.Context, internalName, tenantID string) *triggers.BillingStatus {
	return nil
}

func (m *mockCacheInvalidator) GetClusterPeers(internalName, tenantID string) []*quartermasterpb.TenantClusterPeer {
	return nil
}

func TestResolveVodStorageClusterUsesConfiguredLocalCluster(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("central-primary")
	server.SetStorageResolverFactory(func(ctx context.Context, tenantID string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       "central-primary",
			LocalS3ClientPresent: true,
		}
	})

	cluster, mode := server.resolveVodStorageCluster(context.Background(), "tenant-1", "demo-media")
	if cluster != "central-primary" || mode != storage.StorageMintLocal {
		t.Fatalf("resolveVodStorageCluster() = (%q, %s), want (central-primary, local)", cluster, mode)
	}
}

func TestInvalidateTenantCacheRequiresTenantID(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	_, err := server.InvalidateTenantCache(context.Background(), &foghornpb.InvalidateTenantCacheRequest{})
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

	resp, err := server.InvalidateTenantCache(context.Background(), &foghornpb.InvalidateTenantCacheRequest{
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

	resp, err := server.InvalidateTenantCache(context.Background(), &foghornpb.InvalidateTenantCacheRequest{
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

func TestPlaybackAuthInvalidationIncludesTenantArtifacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT internal_name\\s+FROM foghorn.artifacts").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"internal_name"}).AddRow("asset-a"))

	got := server.tenantArtifactSessionNames(context.Background(), "tenant-1")
	if len(got) != 1 || got[0] != "vod+asset-a" {
		t.Fatalf("tenant artifact session names = %#v, want [vod+asset-a]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestArtifactSessionNodesFallsBackToArtifactPlacement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	server := NewFoghornGRPCServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT artifact_hash\\s+FROM foghorn.artifacts").
		WithArgs("asset-a", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"artifact_hash"}).AddRow("hash-auth-test"))
	mock.ExpectQuery("SELECT DISTINCT an.node_id").
		WithArgs("hash-auth-test", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-a"))

	got := server.artifactSessionNodes(context.Background(), "tenant-1", "vod+asset-a")
	if _, ok := got["node-a"]; !ok || len(got) != 1 {
		t.Fatalf("artifact session nodes = %#v, want node-a", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

	mock.ExpectQuery("SELECT a.artifact_hash, a.artifact_hash, a.status").
		WithArgs("art-1").
		WillReturnError(context.DeadlineExceeded)

	asset, err := server.lookupCompletedUploadAsset("art-1", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if asset.GetArtifactHash() != "art-1" {
		t.Fatalf("expected artifact hash art-1, got %s", asset.GetArtifactHash())
	}
	if asset.GetStatus() != sharedpb.VodStatus_VOD_STATUS_FAILED {
		t.Fatalf("expected failed status, got %v", asset.GetStatus())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
