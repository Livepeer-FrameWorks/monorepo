package control

import (
	"errors"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// SourceDialPlacement is the signed ingest-policy verdict for the exact node
// that would dial a configured source.
type SourceDialPlacement int

const (
	SourceDialPermitted SourceDialPlacement = iota
	SourceDialDenied
	SourceDialUnavailable
)

// SourcePlacementPair joins a local source snapshot into the pair shape the
// placement compiler reads.
func SourcePlacementPair(snapshot localauthority.SourceSnapshot) localauthority.PlacementPair {
	return localauthority.PlacementPair{Tenant: snapshot.Tenant, Object: snapshot.Object}
}

// SourceDialNodePlacement evaluates the pair's ingest policy for one dialing
// node. A schema-1 pair carries no policy and is permitted here; callers keep
// their cluster pin and private-source consent checks in addition. Lifecycle,
// billing or policy denial is a denial. Unready or inconsistent authority and
// missing policy facts (including an unknown node against a node selector) are
// unavailable, never a permission.
func SourceDialNodePlacement(pair localauthority.PlacementPair, clusterID, nodeID string, now time.Time) SourceDialPlacement {
	tenant, object := pair.Tenant.Authority, pair.Object.Authority
	if tenant != nil && object != nil && tenant.GetSchemaVersion() == sharedauthority.SchemaVersion && object.GetSchemaVersion() == sharedauthority.SchemaVersion {
		return SourceDialPermitted
	}
	authority, err := balancer.CompileSourceDialAuthority(pair, now)
	if errors.Is(err, balancer.ErrPlacementAuthorityDenied) {
		return SourceDialDenied
	}
	if err != nil {
		return SourceDialUnavailable
	}
	return sourceDialVerdict(authority.IngestNodeReason(clusterID, nodeID, now))
}

func sourceDialVerdict(reason placement.Reason, err error) SourceDialPlacement {
	switch {
	case err != nil, reason == placement.PolicyFactsUnavailable, reason == placement.UnknownOwnership:
		return SourceDialUnavailable
	case reason == placement.Eligible:
		return SourceDialPermitted
	default:
		return SourceDialDenied
	}
}
