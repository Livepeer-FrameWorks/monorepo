package federation

import (
	"context"
	"errors"
	"reflect"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PlacementIngestFenceReader returns the current ownership constraint, including
// a verified empty result when no publisher holds a cluster. Unknown must error.
// Final publisher admission still requires its atomic ownership claim.
type PlacementIngestFenceReader interface {
	ActiveIngestCluster(context.Context, PlacementIngestIdentity) (string, error)
}

// PlacementIngestIdentity binds the connected ownership read to the exact
// signed object, not just a reusable internal stream name.
type PlacementIngestIdentity struct {
	TenantID     string
	ObjectID     string
	InternalName string
}

// PlacementPolicyGate reuses global observation and policy evaluation without
// invoking preparation. Routing preferences cannot be revalidated from only the
// selected destination's inventory: omitted preferred cells are not empty cells.
type PlacementPolicyGate struct {
	CellID      string
	Authority   PlacementAuthorityReader
	IngestFence PlacementIngestFenceReader
	Router      balancer.PlacementRouter
	Now         func() time.Time
}

type PlacementPolicyValidation struct {
	TenantAuthorityVersion int64
	ObjectAuthorityVersion int64
	Choice                 placement.Choice
	ExpiresAt              time.Time
	Outcome                placementpb.PreparationOutcome
}

func (gate *PlacementPolicyGate) Validate(ctx context.Context, req *placementpb.PreparePlacementRequest) (PlacementPolicyValidation, error) {
	result, err := gate.Assess(ctx, req)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	if result.Outcome != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "selected destination no longer satisfies placement policy")
	}
	return result, nil
}

// Assess separates a fresh exact-node refusal from missing authority, unknown
// capacity, stale telemetry or an unreachable peer. Only explicit observations
// may become refusal acknowledgements that let a coordinator evaluate fallback.
func (gate *PlacementPolicyGate) Assess(ctx context.Context, req *placementpb.PreparePlacementRequest) (PlacementPolicyValidation, error) {
	if gate == nil || gate.CellID == "" || gate.Authority == nil || gate.Router.Observe == nil {
		return PlacementPolicyValidation{}, status.Error(codes.Unavailable, "placement policy revalidation is unavailable")
	}
	if err := placement.ValidatePreparationDeadline(req, gate.now()); err != nil {
		return PlacementPolicyValidation{}, status.Error(codes.InvalidArgument, "invalid placement revalidation request")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	authority, err := gate.readAuthority(ctx, req)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	activeCluster, err := gate.activeIngestCluster(ctx, authority)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	route := balancer.PlacementRouteRequest{
		TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName,
		SourceGeneration: req.Query.SourceGeneration, Verb: authority.Verb, Protocol: req.Query.Protocol,
		Policy: authority.Policy, PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision,
		ParentRevision: authority.ParentRevision, Cells: authority.Cells, ActiveIngestClusterID: activeCluster,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion,
	}
	if location := req.Query.GetClientLocation(); location != nil {
		route.Location = &placement.Coordinates{Latitude: location.Latitude, Longitude: location.Longitude}
	}
	router := gate.Router
	router.Now = gate.now
	evaluated, err := router.Evaluate(ctx, route)
	if err != nil && !errors.Is(err, balancer.ErrPlacementUnavailable) {
		return PlacementPolicyValidation{}, err
	}
	var result PlacementPolicyValidation
	for _, choice := range evaluated.Decision.Choices {
		if choice.ClusterID == req.ClusterId && choice.NodeID == req.NodeId {
			result.Choice = choice
			result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED
			break
		}
	}
	observationExpiry := evaluated.ExpiresAt
	if result.Outcome == placementpb.PreparationOutcome_PREPARATION_OUTCOME_UNSPECIFIED {
		assessment, expiry, known := evaluated.AssessmentFor(req.ClusterId, req.NodeId, gate.now())
		if !known {
			return PlacementPolicyValidation{}, status.Error(codes.Unavailable, "selected destination observation is unavailable")
		}
		switch assessment.Reason {
		case placement.CapacityFull:
			result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED
		case placement.NodeUnavailable:
			result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE
		default:
			return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "selected destination has no current placement permission")
		}
		observationExpiry = expiry
	}
	// Authority or ownership may change while other cells are being observed.
	// Read again before using the decision; neither an old envelope nor an old
	// active-cluster observation may license a newly forbidden destination.
	current, err := gate.readAuthority(ctx, req)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	// Owner consent and commercial facts can change without a tenant policy
	// edit. Comparing only the policy digest would miss those restrictions.
	if !reflect.DeepEqual(authority, current) {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "placement authority facts changed during revalidation")
	}
	currentCluster, err := gate.activeIngestCluster(ctx, current)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	if currentCluster != activeCluster {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "ingest ownership changed during revalidation")
	}
	result.ExpiresAt = req.ExpiresAt.AsTime()
	result.TenantAuthorityVersion = current.TenantAuthorityVersion
	result.ObjectAuthorityVersion = current.ObjectAuthorityVersion
	for _, expiry := range []time.Time{authority.ExpiresAt, current.ExpiresAt, observationExpiry} {
		if expiry.Before(result.ExpiresAt) {
			result.ExpiresAt = expiry
		}
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementPolicyValidation{}, status.FromContextError(contextErr).Err()
	}
	if !gate.now().Before(result.ExpiresAt) {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "placement revalidation expired")
	}
	return result, nil
}

func (gate *PlacementPolicyGate) readAuthority(ctx context.Context, req *placementpb.PreparePlacementRequest) (balancer.PlacementAuthority, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	query := req.GetQuery()
	pair, err := gate.Authority.Placement(ctx, query.TenantId, query.ObjectId, query.InternalName)
	if contextErr := ctx.Err(); contextErr != nil {
		return balancer.PlacementAuthority{}, status.FromContextError(contextErr).Err()
	}
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	verb := placement.Serve
	if query.Verb == placementpb.Verb_VERB_INGEST {
		verb = placement.Ingest
	}
	authority, err := balancer.CompilePlacementAuthority(pair, verb, gate.now())
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	if !authority.MatchesQuery(query, gate.CellID) {
		return balancer.PlacementAuthority{}, status.Error(codes.FailedPrecondition, "placement authority changed or belongs to another cell")
	}
	return authority, nil
}

func (gate *PlacementPolicyGate) activeIngestCluster(ctx context.Context, authority balancer.PlacementAuthority) (string, error) {
	if authority.Verb != placement.Ingest {
		return "", nil
	}
	if gate.IngestFence == nil {
		return "", status.Error(codes.Unavailable, "ingest ownership is unavailable")
	}
	return gate.IngestFence.ActiveIngestCluster(ctx, PlacementIngestIdentity{TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName})
}

func (gate *PlacementPolicyGate) now() time.Time {
	if gate.Now != nil {
		return gate.Now().UTC()
	}
	return time.Now().UTC()
}
