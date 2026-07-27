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

// ClusterAccessibleForTenant is the generic cluster↔tenant entitlement predicate: platform-shared clusters
// serve any tenant; otherwise the cluster must be the tenant's official cluster or an active peer; everything
// else (empty inputs, QM unavailable, non-member) fails closed.
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
		{"platform-shared serves any RESOLVED tenant", "cluster-shared", "tenant-a", true},
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
}
