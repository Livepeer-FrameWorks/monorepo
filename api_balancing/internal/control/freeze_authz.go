package control

import (
	"context"
	"strings"

	"frameworks/api_balancing/internal/storage"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// tenantStorageRouting is a tenant's server-owned cluster-entitlement envelope: its platform-official
// cluster plus the active, UNEXPIRED tenant_cluster_access peer set (the Quartermaster peer query filters to
// active + unexpired grants). It authorizes serving and workload placement (see ClusterAccessibleForTenant);
// it does NOT choose a durable storage destination, which is always the artifact's origin cluster.
type tenantStorageRouting struct {
	officialCluster string
	peers           []*clusterpeerpb.TenantClusterPeer
}

// resolveTenantStorageRouting fetches the tenant's official cluster + active peer set from Quartermaster.
// Returns ok=false on any error/absence so callers FAIL CLOSED: without a positive entitlement proof, the
// tenant's workload is not authorized on a cluster.
func resolveTenantStorageRouting(ctx context.Context, tenantID string) (tenantStorageRouting, bool) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || quartermasterClient == nil {
		return tenantStorageRouting{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, clusterAccessLookupBudget)
	defer cancel()
	routing, err := quartermasterClient.GetClusterRouting(rctx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
	if err != nil || routing == nil {
		return tenantStorageRouting{}, false
	}
	// Quartermaster OMITS official_cluster_id when the official cluster equals the tenant's primary cluster
	// (the common config), so normalize to the primary cluster id in that case — otherwise those tenants
	// would fail closed with no destination.
	official := strings.TrimSpace(routing.GetOfficialClusterId())
	if official == "" {
		official = strings.TrimSpace(routing.GetClusterId())
	}
	return tenantStorageRouting{
		officialCluster: official,
		peers:           routing.GetClusterPeers(),
	}, true
}

// tenantStorageRoutingFn is the seam tests use to inject an entitlement envelope without a live
// Quartermaster. Defaults to the real resolver.
var tenantStorageRoutingFn = resolveTenantStorageRouting

// canMintOriginLocallyFn reports whether the artifact's origin cluster's durable-storage backing is THIS cell's
// local S3 backing — i.e. this cell stores, and can verify, that cluster's bytes. The tenant is required because
// the production advertised-backing lookup is tenant-scoped. Seam for tests.
var canMintOriginLocallyFn = defaultCanMintOriginLocally

func defaultCanMintOriginLocally(ctx context.Context, tenantID, origin string) bool {
	origin = strings.TrimSpace(origin)
	if s3Client == nil || origin == "" {
		return false
	}
	if storageResolverFactory != nil {
		if resolver := storageResolverFactory(ctx, tenantID); resolver != nil {
			dest, mode := resolver.ResolveOriginDurable(origin)
			return dest == origin && mode == storage.StorageMintLocal
		}
	}
	// No resolver (minimal/dev): only this Foghorn's own configured cluster is locally mintable.
	return origin == localClusterID
}

// authorizeStorageReplication is the source/destination gate for a freeze that mints a NEW overwrite-capable
// PUT into durable storage. The destination is the artifact's ORIGIN cluster (the cluster that produced it), and
// the only nodes that may upload into it are nodes of that same cluster: the node's cluster is its
// server-owned, authenticated binding, and the origin is recorded server-side when the artifact is created.
// A node elsewhere (a warm copy on another cluster, a BYOC node of a different cluster) can never obtain a PUT
// into the origin's storage. Fails closed on an unknown origin or unresolved node cluster.
func authorizeStorageReplication(nodeCluster, originCluster string) bool {
	nodeCluster = strings.TrimSpace(nodeCluster)
	originCluster = strings.TrimSpace(originCluster)
	return originCluster != "" && nodeCluster == originCluster
}
