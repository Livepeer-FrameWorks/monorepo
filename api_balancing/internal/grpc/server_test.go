package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestInvalidatePlaybackAuthIgnoresRetiredBundleRevocation(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	resp, err := server.InvalidatePlaybackAuth(context.Background(), &foghornpb.InvalidatePlaybackAuthRequest{
		TenantId: "tenant-1", Reason: "bundle_revoke", StreamId: "stream-1", BundleMinVersion: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStreamsInvalidated() != 0 || resp.GetNodesAttempted() != 0 {
		t.Fatalf("retired bundle invalidation caused session churn: %+v", resp)
	}
}

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

func (m *mockCacheInvalidator) GetClusterPeers(internalName, tenantID string) []*clusterpeerpb.TenantClusterPeer {
	return nil
}

// euCellResolver models the EU cell's Foghorn: it serves platform-eu with its own S3 backend; platform-us is
// another cell with a different backend.
func euCellResolver(_ context.Context, _ string) *storage.ClusterResolver {
	euBacking := storage.S3Backing{Bucket: "frameworks-eu", Endpoint: "https://eu.example", Region: "eu-central-1"}
	return &storage.ClusterResolver{
		LocalClusterID:       "platform-eu",
		LocalClusterServed:   func(id string) bool { return id == "platform-eu" },
		LocalS3Backing:       euBacking,
		LocalS3ClientPresent: true,
		AdvertisedBacking: func(id string) (storage.S3Backing, bool) {
			switch id {
			case "platform-eu":
				return euBacking, true
			case "platform-us":
				return storage.S3Backing{Bucket: "frameworks-us", Endpoint: "https://us.example", Region: "us-east-1"}, true
			}
			return storage.S3Backing{}, false
		},
	}
}

// A VOD accepted by the EU cluster is stored in the EU cell. No tenant-level cluster is consulted: the server
// has no Quartermaster wired at all.
func TestVodOriginStorage_AcceptingClusterIsLocal(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("platform-eu")
	server.SetStorageResolverFactory(euCellResolver)

	if err := server.vodOriginStorage(context.Background(), "tenant-1", "platform-eu"); err != nil {
		t.Fatalf("vodOriginStorage(platform-eu) = %v, want nil", err)
	}
}

// A create for a cluster whose storage lives in another cell is a misroute: this cell must neither store it
// locally nor pretend to delegate the multipart lifecycle.
func TestVodOriginStorage_OriginInAnotherCellRefused(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("platform-eu")
	server.SetStorageResolverFactory(euCellResolver)

	err := server.vodOriginStorage(context.Background(), "tenant-1", "platform-us")
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("vodOriginStorage(platform-us) code = %s (%v), want FailedPrecondition", status.Code(err), err)
	}
}

func TestVodOriginStorage_MissingClusterIDFailsClosed(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetStorageResolverFactory(euCellResolver)

	if err := server.vodOriginStorage(context.Background(), "tenant-1", " "); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty cluster_id code = %s, want InvalidArgument", status.Code(err))
	}
}

func TestVodOriginStorage_NilResolverFailsClosed(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("platform-eu")

	if err := server.vodOriginStorage(context.Background(), "tenant-1", "platform-eu"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("nil resolver code = %s, want FailedPrecondition", status.Code(err))
	}
}

func TestInvalidateTenantCacheRequiresTenantID(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	_, err := server.InvalidateTenantCache(context.Background(), &foghorncontrolpb.InvalidateTenantCacheRequest{})
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

	resp, err := server.InvalidateTenantCache(context.Background(), &foghorncontrolpb.InvalidateTenantCacheRequest{
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

	resp, err := server.InvalidateTenantCache(context.Background(), &foghorncontrolpb.InvalidateTenantCacheRequest{
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

	mock.ExpectQuery("SELECT a.artifact_hash AS id, a.artifact_hash, a.status").
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
