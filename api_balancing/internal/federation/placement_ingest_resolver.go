package federation

import (
	"context"
	"errors"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// IngestPlacementResolver binds front-door identity to signed policy and current
// ownership before using the same discovery/preparation router from any cell.
type IngestPlacementResolver struct {
	Authority PlacementAuthorityReader
	Fence     PlacementIngestFenceReader
	Router    balancer.PlacementRouter
}

var _ control.IngestPlacementPreparer = (*IngestPlacementResolver)(nil)

func (resolver *IngestPlacementResolver) PrepareIngest(ctx context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
	if resolver == nil || resolver.Authority == nil || resolver.Fence == nil {
		return balancer.PlacementPreparationResult{}, errors.New("ingest placement resolver is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if !validPullIdentity(request.TenantID) || !validPullIdentity(request.StreamID) || !validPullIdentity(request.InternalName) ||
		(request.Protocol != "whip" && request.Protocol != "rtmp" && request.Protocol != "srt") {
		return balancer.PlacementPreparationResult{}, errors.New("ingest placement requires exact publisher identity and protocol")
	}
	identity := PlacementIngestIdentity{TenantID: request.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID), InternalName: request.InternalName}
	read := func() (balancer.PlacementAuthority, error) {
		readCtx, stopRead := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
		defer stopRead()
		pair, err := resolver.Authority.Placement(readCtx, identity.TenantID, identity.ObjectID, identity.InternalName)
		if err != nil {
			return balancer.PlacementAuthority{}, err
		}
		if err := readCtx.Err(); err != nil {
			return balancer.PlacementAuthority{}, err
		}
		return balancer.CompilePlacementAuthority(pair, placement.Ingest, time.Now())
	}
	authority, err := read()
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if authority.TenantID != identity.TenantID || authority.ObjectID != identity.ObjectID || authority.InternalName != identity.InternalName || authority.IngestMode != "push" {
		return balancer.PlacementPreparationResult{}, errors.New("ingest placement authority identity differs")
	}
	// A route stands only on the snapshot it was decided on. A snapshot that
	// changed while routing is routed again, so only a policy that forbids
	// every destination refuses the publisher.
	for round := 1; ; round++ {
		owner, err := resolver.Fence.ActiveIngestCluster(ctx, identity)
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		result, err := resolver.Router.Route(ctx, balancer.PlacementRouteRequest{TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName,
			Verb: placement.Ingest, Protocol: request.Protocol, Policy: authority.Policy, PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision,
			ParentRevision: authority.ParentRevision, Cells: authority.Cells, Location: request.Location, ActiveIngestClusterID: owner,
			TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion})
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		current, err := read()
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		if err := ctx.Err(); err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		if placementDecisionFactsEqual(authority, current) {
			return result.Preparation, nil
		}
		if round == placementAuthorityRounds {
			return balancer.PlacementPreparationResult{}, &placementAuthorityChangedError{rounds: round}
		}
		authority = current
	}
}
