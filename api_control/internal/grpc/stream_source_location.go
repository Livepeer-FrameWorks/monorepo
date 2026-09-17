package grpc

import (
	"context"
	"errors"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var errPrivateSourceUnbounded = errors.New("private pull source is not bounded to consented clusters")

// streamPlacementPlan is the placement side of a stream create/update, prepared
// with owner-service reads before the write transaction opens.
type streamPlacementPlan struct {
	// write replaces the stream's own ingest constraints with location.
	write    bool
	location placementpolicy.SourceLocation
	// private requires the effective ingest policy to confine the source to
	// clusters in consented.
	private   bool
	consented map[string]bool
}

func (p *streamPlacementPlan) needsApply() bool {
	return p != nil && (p.write || p.private)
}

func classifyPullSourceURI(rawURI string) (pullsource.Class, error) {
	class, err := pullsource.Classify(rawURI)
	if class == pullsource.ClassBlocked {
		if err == nil {
			err = errors.New("source_uri rejected")
		}
		return class, status.Errorf(codes.InvalidArgument, "invalid pull source: %v", err)
	}
	return class, nil
}

func sourceLocationFromProto(in *commodorepb.StreamSourceLocation) (placementpolicy.SourceLocation, error) {
	location := placementpolicy.SourceLocation{AvoidNodeIDs: in.GetAvoidNodeIds()}
	switch in.GetMode() {
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY:
		location.Mode = placementpolicy.SourceLocationAny
	case commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED:
		location.Mode = placementpolicy.SourceLocationRestricted
	default:
		return placementpolicy.SourceLocation{}, status.Error(codes.InvalidArgument, "source_location.mode must be ANY or RESTRICTED")
	}
	for _, cluster := range in.GetClusters() {
		location.Clusters = append(location.Clusters, placementpolicy.SourceLocationCluster{ClusterID: cluster.GetClusterId(), NodeIDs: cluster.GetNodeIds()})
	}
	canonical, err := placementpolicy.CanonicalSourceLocation(location)
	if err != nil {
		return placementpolicy.SourceLocation{}, status.Error(codes.InvalidArgument, err.Error())
	}
	return canonical, nil
}

// legacyPinsSourceLocation maps the pull_source.allowed_clusters input onto the
// equivalent source location: no pins is ANY, pins restrict to those clusters.
func legacyPinsSourceLocation(pins []string) (placementpolicy.SourceLocation, error) {
	pins = normalizeAllowedClusterIDs(pins)
	if len(pins) == 0 {
		return placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationAny}, nil
	}
	location := placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationRestricted}
	for _, pin := range pins {
		location.Clusters = append(location.Clusters, placementpolicy.SourceLocationCluster{ClusterID: pin})
	}
	canonical, err := placementpolicy.CanonicalSourceLocation(location)
	if err != nil {
		return placementpolicy.SourceLocation{}, status.Error(codes.InvalidArgument, err.Error())
	}
	return canonical, nil
}

// legacyPinColumn is the value mirrored into the NOT NULL pin column: the
// restricted clusters, or an empty list (never nil, which encodes as NULL).
func legacyPinColumn(location placementpolicy.SourceLocation) []string {
	return normalizeAllowedClusterIDs(location.ClusterIDs())
}

func sourceLocationToProto(location placementpolicy.SourceLocation) *commodorepb.StreamSourceLocation {
	out := &commodorepb.StreamSourceLocation{Mode: commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_ANY}
	switch location.Mode {
	case placementpolicy.SourceLocationRestricted:
		out.Mode = commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_RESTRICTED
	case placementpolicy.SourceLocationCustom:
		out.Mode = commodorepb.SourceLocationMode_SOURCE_LOCATION_MODE_CUSTOM
		return out
	}
	for _, cluster := range location.Clusters {
		out.Clusters = append(out.Clusters, &commodorepb.SourceLocationCluster{ClusterId: cluster.ClusterID, NodeIds: cluster.NodeIDs})
	}
	out.AvoidNodeIds = location.AvoidNodeIDs
	return out
}

// prepareStreamPlacement validates a requested source location (or the legacy
// pin input it replaces) against the tenant's entitled edge clusters, node
// ownership and node-placement attestation, and captures the clusters that
// consent to private pull sources when the source is private. Entitlement
// failures name no cluster metadata beyond the caller's own input.
func (s *CommodoreServer) prepareStreamPlacement(ctx context.Context, tenantID string, locationInput *commodorepb.StreamSourceLocation, legacyPins *commodorepb.PullSourceAllowedClustersInput, privateSource bool) (*streamPlacementPlan, error) {
	if locationInput != nil && legacyPins != nil {
		return nil, status.Error(codes.InvalidArgument, "set source_location or pull_source.allowed_clusters, not both")
	}
	plan := &streamPlacementPlan{private: privateSource}
	var err error
	switch {
	case locationInput != nil:
		plan.location, err = sourceLocationFromProto(locationInput)
		plan.write = true
	case legacyPins != nil:
		plan.location, err = legacyPinsSourceLocation(legacyPins.GetClusterIds())
		plan.write = true
	}
	if err != nil {
		return nil, err
	}
	if !plan.needsApply() {
		return plan, nil
	}
	if s.quartermasterClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "cannot validate stream source location: Quartermaster unavailable")
	}
	capabilities, err := s.listPullSourceClusterCapabilities(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	eligible := make(map[string]bool, len(capabilities))
	consented := make(map[string]bool, len(capabilities))
	for _, capability := range capabilities {
		eligible[capability.ID] = true
		consented[capability.ID] = capability.AllowPrivatePullSources
	}
	for _, clusterID := range plan.location.ClusterIDs() {
		if !eligible[clusterID] {
			return nil, status.Errorf(codes.InvalidArgument, "source location cluster %q is not eligible for this stream", clusterID)
		}
	}
	if plan.private {
		plan.consented = consented
	}
	if plan.write && len(plan.location.NodeIDs()) != 0 {
		facts, factsErr := s.mediaPlacementOwnerFacts(ctx, tenantID)
		if factsErr != nil {
			return nil, mediaPlacementError(factsErr)
		}
		rules := &placementpb.PolicySet{Ingest: &placementpb.Rules{SchemaVersion: placement.SchemaVersion, Constraints: placementpolicy.SourceLocationConstraints(plan.location)}}
		if nodeErr := s.checkMediaPlacementNodeSelectors(ctx, tenantID, rules, facts); nodeErr != nil {
			return nil, nodeErr
		}
	}
	return plan, nil
}

// applyStreamPlacement writes the plan inside the stream write transaction. The
// private-source rule is evaluated against the locked tenant policy and the
// stream's resulting own rules, so a concurrent tenant change cannot slip
// between validation and commit.
func applyStreamPlacement(ctx context.Context, exec commodoredb.DBTX, tenantID, streamID, actorID string, plan *streamPlacementPlan) (placementpolicy.SystemApplyResult, error) {
	result, err := placementpolicy.ApplySystem(ctx, exec, placementpolicy.SystemApplyInput{
		Scope:   placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID},
		ActorID: actorID,
		Update: func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) {
			if !plan.write {
				return nil, nil
			}
			return placementpolicy.SourceLocationUpdate(own, plan.location, false)
		},
		Validate: func(locked placementpolicy.Snapshot, next *placementpb.PolicySet) error {
			if !plan.private {
				return nil
			}
			policy, compileErr := placement.CompilePolicySets(locked.Parent, next, placement.Ingest)
			if compileErr != nil || !placementpolicy.PrivateSourceBounded(policy, plan.consented) {
				return errPrivateSourceUnbounded
			}
			return nil
		},
	})
	return result, streamPlacementError(err)
}

func streamPlacementError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errPrivateSourceUnbounded):
		return status.Error(codes.InvalidArgument, "private or multicast pull sources require a source location restricted to clusters that allow private pull sources")
	case errors.Is(err, placementpolicy.ErrSourceLocationCustom):
		return status.Error(codes.FailedPrecondition, "this stream's ingest placement has custom rules; change it through media placement")
	case errors.Is(err, placementpolicy.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, placementpolicy.ErrNotFound):
		return status.Error(codes.NotFound, "stream not found")
	case errors.Is(err, placementpolicy.ErrRevisionConflict), errors.Is(err, placementpolicy.ErrIdempotencyConflict):
		return status.Error(codes.Aborted, "stream placement changed concurrently; retry")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Internal, "failed to write stream source location")
	}
}

// attachStreamSourceLocations reads every listed stream's own policy in one
// tenant-scoped query. A read failure fails the stream read rather than
// reporting an unrestricted location.
func (s *CommodoreServer) attachStreamSourceLocations(ctx context.Context, tenantID string, streams []*commodorepb.Stream) error {
	if len(streams) == 0 {
		return nil
	}
	ids := make([]string, 0, len(streams))
	for _, stream := range streams {
		ids = append(ids, stream.GetStreamId())
	}
	policies, err := placementpolicy.StreamPolicies(ctx, s.db, tenantID, ids)
	if err != nil {
		return status.Errorf(codes.Internal, "read stream placement: %v", err)
	}
	for _, stream := range streams {
		stream.SourceLocation = sourceLocationToProto(placementpolicy.SourceLocationOf(policies[stream.GetStreamId()]))
	}
	return nil
}
