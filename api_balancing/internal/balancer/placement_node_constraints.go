package balancer

import (
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// IngestNodeReason checks one exact node against this ingest authority's hard
// constraints. It does not rank, reserve or assert capacity. A cluster without
// ingest entitlement or owner ingest consent is NotEntitled, and an empty node
// ID cannot satisfy or escape a selector that names nodes.
func (authority PlacementAuthority) IngestNodeReason(clusterID, nodeID string, now time.Time) (placement.Reason, error) {
	if authority.Verb != placement.Ingest {
		return "", ErrPlacementAuthorityInvalid
	}
	facts, entitled := authority.Clusters[clusterID]
	if !entitled {
		return placement.NotEntitled, nil
	}
	return placement.CheckConstraints(placement.Request{TenantID: authority.TenantID, Verb: placement.Ingest, Policy: authority.Policy, Now: now}, placement.Candidate{
		TenantID: authority.TenantID, ClusterID: clusterID, NodeID: nodeID, OwnerTenantID: facts.OwnerTenantID, Official: facts.Official,
		Region: facts.Region, AllowedVerbs: facts.AllowedVerbs, Charging: facts.Charging, ChargingRevision: facts.ChargingRevision, ChargingUntil: facts.ChargingUntil,
	})
}
