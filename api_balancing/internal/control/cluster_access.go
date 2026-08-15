package control

import (
	"context"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
)

// clusterAccessCache memoizes the tenant entitlement envelope so serve-path authorization (the hot playback
// path) does not hit Quartermaster on every check. Short TTL so revocations propagate quickly; a resolver
// miss/error is cached only briefly (NegativeTTL) to avoid hammering QM during an outage while still failing
// closed. A package var so tests can install a fresh cache for isolation.
const clusterAccessCacheTTL = 30 * time.Second

var clusterAccessCache = cache.New(cache.Options{
	TTL:                  clusterAccessCacheTTL,
	StaleWhileRevalidate: 0,
	NegativeTTL:          5 * time.Second,
	MaxEntries:           10000,
}, cache.MetricsHooks{})

// cachedClusterRouting returns the tenant's entitlement envelope through clusterAccessCache. Fail-closed: a
// resolver miss/error yields ok=false (cached briefly), so callers deny rather than leak.
func cachedClusterRouting(tenantID string) (tenantStorageRouting, bool) {
	v, ok, err := clusterAccessCache.Get(context.Background(), "access:"+tenantID, func(loadCtx context.Context, _ string) (interface{}, bool, error) {
		routing, rok := tenantStorageRoutingFn(loadCtx, tenantID)
		if !rok {
			return tenantStorageRouting{}, false, nil
		}
		return routing, true, nil
	})
	if err != nil || !ok {
		return tenantStorageRouting{}, false
	}
	r, rok := v.(tenantStorageRouting)
	return r, rok
}

// ClusterAccessibleForTenant reports whether a physical node's virtual cluster is entitled to run a tenant's
// media workload, per Quartermaster cluster↔tenant entitlement. It reads RAW grants: unlike the cluster-peer
// envelope Commodore returns on a resolve, it applies neither the tenant's plan classes nor cluster health, so
// paths that have a freshly-resolved envelope (playback, ingest endpoint selection) authorize against that
// instead. This is for the paths that have no such envelope — storage placement, freeze, job routing. It is the
// GENERIC access predicate the authority model is built on: the security boundary is the authenticated node→cluster binding plus this
// entitlement, NOT any NodeState.TenantID string. A platform-shared edge cluster is entitled to any tenant.
// Otherwise the cluster must be the tenant's official cluster or a member of its active, unexpired
// cluster_cluster_access peer set (materialized by Quartermaster's GetClusterRouting → ClusterPeers, which
// already filters to active clusters + unexpired grants). Fail CLOSED: an empty cluster/tenant, or a
// Quartermaster that cannot be reached, yields false — an unproven cluster never runs a tenant's media.
//
// Operation policy (serve / store-durable / process) layers SEPARATELY on top of this predicate; the
// predicate itself carries no operation scope. Node capability/capacity (CapProcessing, CanRunClass, slots)
// remain scheduler input, never authorization.
func ClusterAccessibleForTenant(clusterID, tenantID string) bool {
	clusterID = strings.TrimSpace(clusterID)
	tenantID = strings.TrimSpace(tenantID)
	// FAIL CLOSED on an unresolved tenant BEFORE any cluster check: an empty tenant is NOT "all tenants". A
	// platform-shared cluster is entitled to any RESOLVED tenant, never to an unattributed (tenantless) workload.
	if clusterID == "" || tenantID == "" {
		return false
	}
	// A platform-shared edge cluster is entitled to any (resolved) tenant's workload.
	if IsPlatformSharedCluster(clusterID) {
		return true
	}
	// Reuse the freeze path's entitlement envelope (the tenant's official cluster + active, unexpired peer set
	// from GetClusterRouting) — the "storage" naming is incidental; the peer membership IS the generic
	// cluster↔tenant entitlement with no per-operation scope. Read it through the short-TTL cache so repeated
	// checks don't hit Quartermaster per request.
	routing, ok := cachedClusterRouting(tenantID)
	if !ok {
		return false
	}
	if clusterID == strings.TrimSpace(routing.officialCluster) {
		return true
	}
	for _, peer := range routing.peers {
		if strings.TrimSpace(peer.GetClusterId()) == clusterID {
			return true
		}
	}
	return false
}
