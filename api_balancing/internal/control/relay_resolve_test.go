package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// nodeMayServeTenant authorizes serving by the AUTHENTICATED node's cluster ↔ tenant entitlement
// (ClusterServeAccessibleForTenant): a platform-shared edge serves any resolved tenant; a dedicated cluster serves only the
// tenants Quartermaster entitles it to (its owner + granted peers). node.TenantID is no longer the authority;
// an empty/unentitled/unresolved cluster fails closed.
func TestNodeMayServeTenant_AuthorityModel(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	ctx := context.Background()

	freshClusterAccessCache(t)
	prevCfg := platformSharedConfig.Load()
	prevFn := tenantStorageRoutingFn
	t.Cleanup(func() {
		platformSharedConfig.Store(prevCfg)
		tenantStorageRoutingFn = prevFn
	})
	platformSharedConfig.Store(&sync.Map{})
	AddPlatformSharedCluster("platform-eu")

	// Quartermaster entitlement: tenant-a's dedicated cluster is "cluster-a"; no other cluster is entitled to it,
	// and tenant-b is entitled to nothing here.
	tenantStorageRoutingFn = func(_ context.Context, tenantID string) (tenantStorageRouting, bool) {
		if tenantID == "tenant-a" {
			return tenantStorageRouting{officialCluster: "cluster-a"}, true
		}
		return tenantStorageRouting{}, true
	}

	// Dedicated node on tenant-a's entitled cluster.
	sm.SetNodeConnectionInfo(ctx, "dedicated-a", "n", "tenant-a", "cluster-a", nil)
	// Node on an EXPLICITLY platform-shared cluster.
	sm.SetNodeConnectionInfo(ctx, "platform-edge", "n", "", "platform-eu", nil)
	// Node on a cluster this Foghorn SERVES but which is neither platform-shared nor entitled to tenant-a.
	sm.SetNodeConnectionInfo(ctx, "served-only-edge", "n", "", "served-byoc", nil)
	AddServedCluster("served-byoc")
	// Node on a cluster neither served, designated, nor entitled.
	sm.SetNodeConnectionInfo(ctx, "foreign-edge", "n", "", "foreign-cluster", nil)
	// Node with no cluster identity at all (e.g. a QM-outage window before cluster resolution).
	sm.SetNodeConnectionInfo(ctx, "bare-edge", "n", "tenant-a", "", nil)

	cases := []struct {
		name, node, tenant string
		want               bool
	}{
		{"empty artifact tenant denied", "platform-edge", "", false},
		{"unknown node denied", "ghost", "tenant-a", false},
		{"dedicated cluster serves its entitled tenant", "dedicated-a", "tenant-a", true},
		{"dedicated cluster denied other tenant", "dedicated-a", "tenant-b", false},
		{"platform-shared edge serves any tenant", "platform-edge", "tenant-a", true},
		{"served-but-not-entitled denied", "served-only-edge", "tenant-a", false},
		{"unentitled foreign cluster denied", "foreign-edge", "tenant-a", false},
		{"unresolved (empty) cluster denied even with matching node tenant", "bare-edge", "tenant-a", false},
	}
	for _, tc := range cases {
		if got := nodeMayServeTenant(tc.node, tc.tenant); got != tc.want {
			t.Errorf("%s: nodeMayServeTenant(%q,%q) = %v, want %v", tc.name, tc.node, tc.tenant, got, tc.want)
		}
	}
}

// Exercises the REAL refresh logic (applyPlatformSharedRefresh + IsPlatformSharedCluster): active-official
// filtering, atomic replacement on refresh (so a revoked cluster stops being authorized), the separate
// never-revoked explicit-config set, and the hard-expiry of the derived snapshot.
func TestPlatformSharedClusters_RefreshFilterRevokeExpire(t *testing.T) {
	prevCfg := platformSharedConfig.Load()
	prevDer := platformSharedDerived.Load()
	prevAt := platformSharedDerivedAt.Load()
	t.Cleanup(func() {
		platformSharedConfig.Store(prevCfg)
		platformSharedDerived.Store(prevDer)
		platformSharedDerivedAt.Store(prevAt)
	})
	platformSharedConfig.Store(&sync.Map{})
	platformSharedDerived.Store(&sync.Map{})
	platformSharedDerivedAt.Store(nil)

	cluster := func(id string, official, active bool) *quartermasterpb.InfrastructureCluster {
		return &quartermasterpb.InfrastructureCluster{ClusterId: id, IsPlatformOfficial: official, IsActive: active}
	}

	AddPlatformSharedCluster("cfg-cluster") // explicit, never revoked

	// First refresh: two active-official clusters + one INACTIVE official (must be filtered out).
	applyPlatformSharedRefresh([]*quartermasterpb.InfrastructureCluster{
		cluster("official-a", true, true),
		cluster("official-b", true, true),
		cluster("official-inactive", true, false),
	})
	if !IsPlatformSharedCluster("cfg-cluster") || !IsPlatformSharedCluster("official-a") || !IsPlatformSharedCluster("official-b") {
		t.Fatal("config + active-official clusters must be authorized after refresh")
	}
	if IsPlatformSharedCluster("official-inactive") {
		t.Fatal("an inactive official cluster must NOT be authorized")
	}

	// Second refresh drops official-b (revoked): atomic replace stops authorizing it.
	applyPlatformSharedRefresh([]*quartermasterpb.InfrastructureCluster{cluster("official-a", true, true)})
	if !IsPlatformSharedCluster("official-a") {
		t.Fatal("still-official cluster must remain authorized")
	}
	if IsPlatformSharedCluster("official-b") {
		t.Fatal("revoked cluster must no longer be authorized after refresh")
	}
	if !IsPlatformSharedCluster("cfg-cluster") {
		t.Fatal("explicit-config cluster must survive derived replacement")
	}

	// Hard expiry: age the snapshot past the TTL → the derived set is no longer trusted (fail closed), while
	// the explicit-config set persists.
	expired := time.Now().Add(-2 * platformSharedDerivedTTL)
	platformSharedDerivedAt.Store(&expired)
	if IsPlatformSharedCluster("official-a") {
		t.Fatal("an expired derived snapshot must NOT authorize (hard expiry / fail closed)")
	}
	if !IsPlatformSharedCluster("cfg-cluster") {
		t.Fatal("explicit-config authority must survive derived-snapshot expiry")
	}
}

func TestFillUploadResolveIncludesExpectedSize(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	// Platform/shared edge: no tenant, on a cluster THIS Foghorn operates → entitled to serve the VOD's tenant.
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	sm.SetNodeInfo("node-1", "n", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "node-1", "n", "", "platform-eu", nil)
	AddPlatformSharedCluster("platform-eu")

	mock.ExpectQuery("SELECT vm\\.s3_key, a\\.size_bytes").
		WithArgs("upload-hash").
		WillReturnRows(sqlmock.NewRows([]string{"s3_key", "size_bytes", "tenant_id"}).
			AddRow("uploads/tenant/upload-hash.mov", int64(21708800), "t1"))

	req := &ipcpb.RelayResolveRequest{
		AssetKind: "upload",
		AssetHash: "upload-hash",
	}
	resp := &ipcpb.RelayResolveResponse{}

	fillUploadResolve(context.Background(), req, resp, "node-1", logging.NewLogger())

	if resp.GetState() != ipcpb.AssetState_ASSET_STATE_PLAYABLE {
		t.Fatalf("state = %s, want playable", resp.GetState())
	}
	if got := resp.GetExpectedSizeBytes(); got != 21708800 {
		t.Fatalf("expected_size_bytes = %d, want 21708800", got)
	}
	if resp.GetMediaPresignedUrl() == "" {
		t.Fatal("media presigned URL is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
