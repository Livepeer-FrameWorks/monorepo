package control

import (
	"context"
	"strings"
	"time"

	"frameworks/api_balancing/internal/storage"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// tenantStorageRouting is a tenant's server-owned storage-entitlement envelope: its platform-official
// durable cluster plus the active, UNEXPIRED tenant_cluster_access peer set (the Quartermaster peer query
// filters to active + unexpired grants). Storage authority is derived from this — never from control-cell
// membership (which Foghorn happens to serve a cluster) or generic serving access.
type tenantStorageRouting struct {
	officialCluster string
	peers           []*clusterpeerpb.TenantClusterPeer
}

// resolveTenantStorageRouting fetches the tenant's official cluster + active peer set from Quartermaster.
// Returns ok=false on any error/absence so callers FAIL CLOSED: without a positive entitlement proof, a
// freeze may not mint durable storage.
func resolveTenantStorageRouting(ctx context.Context, tenantID string) (tenantStorageRouting, bool) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || quartermasterClient == nil {
		return tenantStorageRouting{}, false
	}
	rctx, cancel := context.WithTimeout(ctx, 1*time.Second)
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

// tenantStorageRoutingFn is the seam used by the freeze path so tests can inject an entitlement envelope
// without a live Quartermaster. Defaults to the real resolver.
var tenantStorageRoutingFn = resolveTenantStorageRouting

// canMintOfficialLocallyFn reports whether the tenant's official cluster's ADVERTISED durable-storage
// backing equals THIS cell's local S3 backing — i.e. this cell actually stores, and can verify, that
// cluster's bytes. This is the storage-ownership check (NOT control-cell membership). The tenant is required
// because the production advertised-backing lookup is tenant-scoped. Seam for tests.
var canMintOfficialLocallyFn = defaultCanMintOfficialLocally

func defaultCanMintOfficialLocally(ctx context.Context, tenantID, official string) bool {
	official = strings.TrimSpace(official)
	if s3Client == nil || official == "" {
		return false
	}
	// With a resolver wired, require its verdict for the official-ONLY input to be (official, MintLocal) —
	// the resolver compares the cluster's advertised bucket/endpoint/region to this cell's local backing.
	// The factory is tenant-scoped (the backing lookup needs the artifact tenant's context).
	if storageResolverFactory != nil {
		if resolver := storageResolverFactory(ctx, tenantID); resolver != nil {
			dest, mode := resolver.Resolve(storage.ResolverInput{OfficialClusterID: official})
			return dest == official && mode == storage.StorageMintLocal
		}
	}
	// No resolver (minimal/dev): fall back to the strict local-cluster identity — never the broad served set.
	return official == localClusterID
}

// authorizeStorageReplication is the storage-authority gate for a freeze that mints a NEW overwrite-capable
// PUT into the tenant's durable storage. The only authorized durable destination is the tenant's
// PLATFORM-OFFICIAL cluster that THIS Foghorn cell mints AND verifies locally (S3 is that backend's current
// adapter). A subscribed REMOTE storage provider is NOT authorized here (see
// docs/rfcs/cross-cluster-durable-replication-v1.md).
//
// Both sides must hold:
//
//	DESTINATION — destCluster is the tenant's official durable cluster AND the tenant holds ACTIVE,
//	UNEXPIRED access to it (peer-set membership; the peer set is filtered to active/unexpired grants).
//	Generic subscribed/preferred peers are NOT storage destinations.
//
//	SOURCE — the node may replicate this tenant's media: its SERVER-OWNED tenant owns the artifact (the
//	billable BYOC/self-hosted path), or its SERVER-OWNED cluster IS the tenant's official platform cluster.
//	Generic origin-cluster equality does NOT grant source authority: a shared origin cluster can host nodes
//	of other tenants, and inventory possession is self-attested, so origin equality alone would let a node
//	obtain a PUT for a tenant it does not own.
//
// Fails closed on an unknown tenant, an unresolved official cluster, an unresolved node, or any mismatch.
func authorizeStorageReplication(nodeTenant, nodeCluster, artifactTenant, destCluster string, r tenantStorageRouting) bool {
	if artifactTenant == "" || r.officialCluster == "" {
		return false
	}
	// DESTINATION authority: the tenant's official durable backend, with active/unexpired access proven by
	// peer-set membership. This is what excludes generic subscribed/read grants and enforces expiry.
	if destCluster == "" || destCluster != r.officialCluster || !isAuthorizedPeerCluster(destCluster, r.peers) {
		return false
	}
	// SOURCE authority: tenant ownership, or a node ON the tenant's official platform cluster.
	if nodeTenant != "" && nodeTenant == artifactTenant {
		return true
	}
	if nodeCluster != "" && nodeCluster == r.officialCluster {
		return true
	}
	return false
}
