package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

// freshClusterAccessCache installs an isolated entitlement cache for a test and restores the prior one, so the
// short-TTL memoization can't leak resolver stubs between tests.
func freshClusterAccessCache(t *testing.T) {
	t.Helper()
	prev := clusterAccessCache
	clusterAccessCache = cache.New(cache.Options{TTL: clusterAccessCacheTTL, NegativeTTL: 5 * time.Second, MaxEntries: 100}, cache.MetricsHooks{})
	t.Cleanup(func() { clusterAccessCache = prev })
}

// stubClusterEntitlements installs a fresh cache and a Quartermaster-routing stub granting each tenant the given
// clusters (as active peers), so tests that exercise cluster↔tenant serve/placement authorization don't need a
// live Quartermaster. A tenant absent from the map resolves with no entitlement (fail-closed).
func stubClusterEntitlements(t *testing.T, byTenant map[string][]string) {
	t.Helper()
	freshClusterAccessCache(t)
	prev := tenantStorageRoutingFn
	t.Cleanup(func() { tenantStorageRoutingFn = prev })
	tenantStorageRoutingFn = func(_ context.Context, tenantID string) (tenantStorageRouting, bool) {
		clusters, ok := byTenant[tenantID]
		if !ok {
			return tenantStorageRouting{}, true // resolved, but entitled to nothing
		}
		peers := make([]*clusterpeerpb.TenantClusterPeer, 0, len(clusters))
		for _, c := range clusters {
			peers = append(peers, &clusterpeerpb.TenantClusterPeer{ClusterId: c})
		}
		return tenantStorageRouting{peers: peers}, true
	}
}

// ClusterAccessibleForTenant is the generic workload predicate. Platform
// sharing is deliberately confined to ClusterServeAccessibleForTenant.
func TestClusterAccessibleForTenant(t *testing.T) {
	freshClusterAccessCache(t)
	prevCfg := platformSharedConfig.Load()
	prevFn := tenantStorageRoutingFn
	t.Cleanup(func() {
		platformSharedConfig.Store(prevCfg)
		tenantStorageRoutingFn = prevFn
	})
	platformSharedConfig.Store(&sync.Map{})
	AddPlatformSharedCluster("cluster-shared")

	tenantStorageRoutingFn = func(_ context.Context, tenantID string) (tenantStorageRouting, bool) {
		switch tenantID {
		case "tenant-a":
			return tenantStorageRouting{
				officialCluster: "cluster-a-official",
				peers:           []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-a-peer"}},
			}, true
		case "tenant-down":
			return tenantStorageRouting{}, false // Quartermaster unreachable → fail closed
		default:
			return tenantStorageRouting{}, true // resolved, but no entitlement
		}
	}

	cases := []struct {
		name    string
		cluster string
		tenant  string
		want    bool
	}{
		{"empty cluster fails closed", "", "tenant-a", false},
		{"platform-shared does not grant generic workload access", "cluster-shared", "tenant-a", false},
		{"empty tenant fails closed even on a platform-shared cluster", "cluster-shared", "", false},
		{"empty tenant on a non-shared cluster fails closed", "cluster-a-official", "", false},
		{"official cluster is entitled", "cluster-a-official", "tenant-a", true},
		{"active peer cluster is entitled", "cluster-a-peer", "tenant-a", true},
		{"non-member cluster is not entitled", "cluster-x", "tenant-a", false},
		{"Quartermaster unavailable fails closed", "cluster-a-official", "tenant-down", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClusterAccessibleForTenant(tc.cluster, tc.tenant); got != tc.want {
				t.Fatalf("ClusterAccessibleForTenant(%q, %q) = %v, want %v", tc.cluster, tc.tenant, got, tc.want)
			}
		})
	}
	if !ClusterServeAccessibleForTenant("cluster-shared", "tenant-a") {
		t.Fatal("platform-shared playback should be allowed for a resolved tenant")
	}
	if ClusterServeAccessibleForTenant("cluster-shared", "") {
		t.Fatal("platform-shared playback must reject an unresolved tenant")
	}
}

func TestClusterAccessibleForTenantBoundsColdAuthorityLookup(t *testing.T) {
	freshClusterAccessCache(t)
	prev := tenantStorageRoutingFn
	t.Cleanup(func() { tenantStorageRoutingFn = prev })

	tenantStorageRoutingFn = func(ctx context.Context, _ string) (tenantStorageRouting, bool) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > clusterAccessLookupBudget+50*time.Millisecond {
			t.Errorf("cold authority lookup deadline = %v, want at most %v", deadline, clusterAccessLookupBudget)
		}
		<-ctx.Done()
		return tenantStorageRouting{}, false
	}
	started := time.Now()
	if ClusterAccessibleForTenant("cluster", "tenant-hanging") {
		t.Fatal("hanging authority must fail closed")
	}
	if elapsed := time.Since(started); elapsed > clusterAccessLookupBudget+250*time.Millisecond {
		t.Fatalf("cold authority lookup took %v, budget %v", elapsed, clusterAccessLookupBudget)
	}
}

func TestClusterServeAccessibleForTenantEnvelopeIsPureAndFailClosed(t *testing.T) {
	prevCfg := platformSharedConfig.Load()
	prevFn := tenantStorageRoutingFn
	t.Cleanup(func() {
		platformSharedConfig.Store(prevCfg)
		tenantStorageRoutingFn = prevFn
	})
	platformSharedConfig.Store(&sync.Map{})
	AddPlatformSharedCluster("platform-shared")
	loads := 0
	tenantStorageRoutingFn = func(context.Context, string) (tenantStorageRouting, bool) {
		loads++
		return tenantStorageRouting{}, false
	}
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "private-peer"}}

	if !ClusterServeAccessibleForTenantEnvelope("private-peer", "tenant-a", "official-a", peers) {
		t.Fatal("fresh peer envelope denied private serving cluster")
	}
	if !ClusterServeAccessibleForTenantEnvelope("official-a", "tenant-a", "official-a", nil) {
		t.Fatal("fresh envelope denied official serving cluster")
	}
	if !ClusterServeAccessibleForTenantEnvelope("platform-shared", "tenant-a", "", nil) {
		t.Fatal("platform-shared playback policy denied resolved tenant")
	}
	if ClusterServeAccessibleForTenantEnvelopeWithPolicy("platform-shared", "tenant-a", "", nil, false) {
		t.Fatal("signed policy denial was ignored for platform-shared playback")
	}
	if !ClusterServeAccessibleForTenantEnvelopeWithPolicy("platform-shared", "tenant-a", "", nil, true) {
		t.Fatal("signed platform-shared playback grant was ignored")
	}
	if ClusterServeAccessibleForTenantEnvelope("private-peer", "tenant-a", "", nil) {
		t.Fatal("missing envelope authorized private serving cluster")
	}
	if ClusterServeAccessibleForTenantEnvelope("platform-shared", "", "", nil) {
		t.Fatal("unresolved tenant authorized platform-shared serving")
	}
	if loads != 0 {
		t.Fatalf("final-trigger envelope check performed %d authority lookups", loads)
	}
}

func TestClusterServeAccessibleForScopeIsPureAndFailClosed(t *testing.T) {
	prevCfg := platformSharedConfig.Load()
	t.Cleanup(func() { platformSharedConfig.Store(prevCfg) })
	platformSharedConfig.Store(&sync.Map{})
	AddPlatformSharedCluster("platform-shared")
	scope := NewClusterServeScope("tenant-a", "official-a", []*clusterpeerpb.TenantClusterPeer{{ClusterId: "private-peer"}})

	for _, tc := range []struct {
		name, cluster string
		want          bool
	}{
		{"platform shared", "platform-shared", true},
		{"official", "official-a", true},
		{"private peer", "private-peer", true},
		{"unknown", "unknown", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClusterServeAccessibleForScope(tc.cluster, scope); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	if ClusterServeAccessibleForScope("platform-shared", NewClusterServeScope("", "", nil)) {
		t.Fatal("unresolved tenant authorized platform-shared serving")
	}
}
