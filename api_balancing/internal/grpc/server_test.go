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
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
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

// mockQMRouting is a minimal Quartermaster routing resolver for the durable-storage tests. It returns the
// tenant's official cluster (or, when officialClusterID is empty, the primary clusterID — mirroring
// Quartermaster's omit-when-equal-primary behavior) so the strict resolver can positively resolve a destination.
type mockQMRouting struct {
	officialClusterID string
	clusterID         string
}

func (m *mockQMRouting) GetClusterRouting(_ context.Context, _ *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
	resp := &quartermasterpb.ClusterRoutingResponse{ClusterId: m.clusterID}
	if m.officialClusterID != "" {
		resp.OfficialClusterId = &m.officialClusterID
	}
	return resp, nil
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

// When the tenant's official cluster IS this cell (Quartermaster resolves it to central-primary) and this cell
// has an S3 client, the strict durable resolver mints locally — a positive resolution of the official
// destination, not a dev/test fallback.
func TestResolveVodStorageClusterUsesConfiguredLocalCluster(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("central-primary")
	server.SetQuartermasterClient(&mockQMRouting{clusterID: "central-primary"})
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

// I1: a durable VOD write must fail closed when the tenant's official cluster does not positively resolve. With
// no Quartermaster wired the official cluster is unresolved, so the resolver must NOT fall back to the caller's
// ingest cluster or this cell's local cluster — it surfaces StorageUnavailable and mints nothing.
func TestResolveVodStorageCluster_UnresolvedOfficialFailsClosed(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("central-primary")
	server.SetStorageResolverFactory(func(ctx context.Context, tenantID string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       "central-primary",
			LocalS3ClientPresent: true,
		}
	})

	cluster, mode := server.resolveVodStorageCluster(context.Background(), "tenant-1", "byoc-origin")
	if cluster != "" || mode != storage.StorageUnavailable {
		t.Fatalf("unresolved official must fail closed; got (%q, %s), want (\"\", unavailable)", cluster, mode)
	}
}

// I1: a nil resolver factory cannot positively resolve an official cluster, so the durable path fails closed even
// when Quartermaster does return an official cluster.
func TestResolveVodStorageCluster_NilResolverFailsClosed(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("central-primary")
	server.SetQuartermasterClient(&mockQMRouting{clusterID: "central-primary"})

	cluster, mode := server.resolveVodStorageCluster(context.Background(), "tenant-1", "demo-media")
	if cluster != "" || mode != storage.StorageUnavailable {
		t.Fatalf("nil resolver must fail closed; got (%q, %s), want (\"\", unavailable)", cluster, mode)
	}
}

// The durable destination is the tenant's official cluster only: an advertised BYOC INGEST/origin cluster must
// NOT win the durable write, even when it advertises a locally-mintable backing (the pre-fix origin-first bug).
func TestResolveVodStorageCluster_IgnoresOriginAdvertisedBacking(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)
	server.SetClusterID("central-primary")
	server.SetQuartermasterClient(&mockQMRouting{clusterID: "central-primary"})
	server.SetStorageResolverFactory(func(ctx context.Context, tenantID string) *storage.ClusterResolver {
		return &storage.ClusterResolver{
			LocalClusterID:       "central-primary",
			LocalClusterServed:   func(id string) bool { return id == "byoc-origin" },
			LocalS3ClientPresent: true,
			LocalS3Backing:       storage.S3Backing{Bucket: "b", Endpoint: "e", Region: "r"},
			AdvertisedBacking: func(id string) (storage.S3Backing, bool) {
				// The origin advertises a backing this cell could mint locally; official/local advertise nothing.
				// Local minting takes precedence over the origin's advertisement.
				if id == "byoc-origin" {
					return storage.S3Backing{Bucket: "b", Endpoint: "e", Region: "r"}, true
				}
				return storage.S3Backing{}, false
			},
		}
	})

	// The ingest/origin cluster is "byoc-origin"; Quartermaster resolves the official cluster to central-primary.
	// The strict resolver considers ONLY the official cluster — the origin's advertised backing is never queried —
	// so the origin is ignored and the official destination mints locally on central-primary.
	cluster, mode := server.resolveVodStorageCluster(context.Background(), "tenant-1", "byoc-origin")
	if cluster != "central-primary" || mode != storage.StorageMintLocal {
		t.Fatalf("BYOC origin must not win durable destination; got (%q, %s), want (central-primary, local)", cluster, mode)
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
