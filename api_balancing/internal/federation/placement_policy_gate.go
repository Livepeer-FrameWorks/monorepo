package federation

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
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

// ErrPlacementAuthorityChanged reports that signed authority or ingest
// ownership changed on every decision round. Each round re-decides on the
// newer snapshot, so this is not a refusal by policy; the caller retries.
var ErrPlacementAuthorityChanged = errors.New("placement authority kept changing while the decision was made")

// placementAuthorityRounds bounds how often one decision is re-made on a newer
// authority snapshot inside the caller's budget.
const placementAuthorityRounds = 3

type placementAuthorityChangedError struct{ rounds int }

func (err *placementAuthorityChangedError) Error() string {
	return fmt.Sprintf("%s (%d rounds)", ErrPlacementAuthorityChanged.Error(), err.rounds)
}

func (err *placementAuthorityChangedError) Is(target error) bool {
	return target == ErrPlacementAuthorityChanged
}

// GRPCStatus reports the conflict as Aborted: retrying the whole operation
// resolves it, and it is neither a policy refusal nor a missing dependency.
func (err *placementAuthorityChangedError) GRPCStatus() *status.Status {
	return status.New(codes.Aborted, err.Error())
}

// placementDecisionFactsEqual reports whether two snapshots decide placement
// identically. Version counters and the authority lifetime advance with every
// compilation, including the one a publisher's own ownership claim triggers,
// without changing any fact a decision reads.
func placementDecisionFactsEqual(a, b balancer.PlacementAuthority) bool {
	a.TenantAuthorityVersion, a.ObjectAuthorityVersion, a.ExpiresAt = 0, 0, time.Time{}
	b.TenantAuthorityVersion, b.ObjectAuthorityVersion, b.ExpiresAt = 0, 0, time.Time{}
	return reflect.DeepEqual(a, b)
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
	// Request is the preparation restated under the authority that decided
	// it. It differs from the caller's request only when the destination
	// holds newer policy than the coordinator.
	Request *placementpb.PreparePlacementRequest
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
// It evaluates the complete multi-cell census, because it serves final
// admission of connections that did not come through a coordinator.
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
	for round := 1; ; round++ {
		route := gate.route(req, authority, activeCluster, authority.Cells)
		result, observationExpiry, err := gate.decide(ctx, req, route, false)
		if err != nil {
			return PlacementPolicyValidation{}, err
		}
		// Authority or ownership may change while other cells are being
		// observed. A decision stands only on the snapshot it was made on;
		// a changed snapshot is decided again, so only a policy that now
		// forbids the destination refuses it.
		current, err := gate.readAuthority(ctx, req)
		if err != nil {
			return PlacementPolicyValidation{}, err
		}
		currentCluster, err := gate.activeIngestCluster(ctx, current)
		if err != nil {
			return PlacementPolicyValidation{}, err
		}
		if !placementDecisionFactsEqual(authority, current) || currentCluster != activeCluster {
			if round == placementAuthorityRounds {
				return PlacementPolicyValidation{}, &placementAuthorityChangedError{rounds: round}
			}
			authority, activeCluster = current, currentCluster
			continue
		}
		result.Request = req
		result.ExpiresAt = req.ExpiresAt.AsTime()
		result.TenantAuthorityVersion = current.TenantAuthorityVersion
		result.ObjectAuthorityVersion = current.ObjectAuthorityVersion
		for _, expiry := range []time.Time{authority.ExpiresAt, current.ExpiresAt, observationExpiry} {
			if expiry.Before(result.ExpiresAt) {
				result.ExpiresAt = expiry
			}
		}
		return gate.finish(ctx, result)
	}
}

// CheckDestination is the destination side of a preparation. It validates
// only what this cell is authoritative for: its authority is not older than
// the coordinator's, the selected node is live with capacity, and that node's
// own verdict under the signed policy admits it. Preference between cells and
// spillover were decided by the coordinator from its complete census; the
// destination does not repeat that census, so a node the coordinator chose
// from a spillover group is accepted when the node itself is eligible.
func (gate *PlacementPolicyGate) CheckDestination(ctx context.Context, req *placementpb.PreparePlacementRequest) (PlacementPolicyValidation, error) {
	if gate == nil || gate.CellID == "" || gate.Authority == nil || gate.Router.Observe == nil {
		return PlacementPolicyValidation{}, status.Error(codes.Unavailable, "placement destination check is unavailable")
	}
	if err := placement.ValidatePreparationDeadline(req, gate.now()); err != nil {
		return PlacementPolicyValidation{}, status.Error(codes.InvalidArgument, "invalid placement preparation request")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	pair, err := gate.readPair(ctx, req)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placementVerb(req.GetQuery()), gate.now())
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	localQuery, err := authority.PreparationQuery(req.GetQuery(), req.GetClusterId(), gate.CellID)
	if err != nil {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "destination cannot prepare under its authority: "+err.Error())
	}
	local := proto.CloneOf(req)
	local.Query = localQuery
	activeCluster, err := gate.activeIngestCluster(ctx, authority)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	cell := balancer.PlacementCell{ID: gate.CellID, ClusterIDs: localQuery.GetClusterIds()}
	route := gate.route(local, authority, activeCluster, []balancer.PlacementCell{cell})
	result, observationExpiry, err := gate.decide(ctx, req, route, true)
	if err != nil {
		return PlacementPolicyValidation{}, err
	}
	result.Request = local
	result.ExpiresAt = req.ExpiresAt.AsTime()
	result.TenantAuthorityVersion = authority.TenantAuthorityVersion
	result.ObjectAuthorityVersion = authority.ObjectAuthorityVersion
	for _, expiry := range []time.Time{authority.ExpiresAt, observationExpiry} {
		if expiry.Before(result.ExpiresAt) {
			result.ExpiresAt = expiry
		}
	}
	return gate.finish(ctx, result)
}

func (gate *PlacementPolicyGate) route(req *placementpb.PreparePlacementRequest, authority balancer.PlacementAuthority, activeCluster string, cells []balancer.PlacementCell) balancer.PlacementRouteRequest {
	route := balancer.PlacementRouteRequest{
		TenantID: authority.TenantID, ObjectID: authority.ObjectID, InternalName: authority.InternalName,
		SourceGeneration: req.Query.SourceGeneration, Verb: authority.Verb, Protocol: req.Query.Protocol,
		Policy: authority.Policy, PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision,
		ParentRevision: authority.ParentRevision, Cells: cells, ActiveIngestClusterID: activeCluster,
		TenantAuthorityVersion: authority.TenantAuthorityVersion, ObjectAuthorityVersion: authority.ObjectAuthorityVersion,
	}
	if location := req.Query.GetClientLocation(); location != nil {
		route.Location = &placement.Coordinates{Latitude: location.Latitude, Longitude: location.Longitude}
	}
	return route
}

// decide evaluates the route and reports the selected node's outcome with the
// expiry of the evidence behind it. With nodeVerdict set, an eligible node is
// accepted whether or not it ranks among the choices; otherwise the node must
// be one of the route's choices.
func (gate *PlacementPolicyGate) decide(ctx context.Context, req *placementpb.PreparePlacementRequest, route balancer.PlacementRouteRequest, nodeVerdict bool) (PlacementPolicyValidation, time.Time, error) {
	router := gate.Router
	router.Now = gate.now
	evaluated, err := router.Evaluate(ctx, route)
	if err != nil && !errors.Is(err, balancer.ErrPlacementUnavailable) {
		return PlacementPolicyValidation{}, time.Time{}, err
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
	if result.Outcome != placementpb.PreparationOutcome_PREPARATION_OUTCOME_UNSPECIFIED {
		return result, observationExpiry, nil
	}
	assessment, expiry, known := evaluated.AssessmentFor(req.ClusterId, req.NodeId, gate.now())
	if !known {
		return PlacementPolicyValidation{}, time.Time{}, status.Errorf(codes.Unavailable, "selected destination %s/%s observation is unavailable: %s",
			req.ClusterId, req.NodeId, evaluated.UnassessedReason(req.ClusterId, req.NodeId, gate.now()))
	}
	switch assessment.Reason {
	case placement.Eligible, placement.Selected:
		if !nodeVerdict {
			return PlacementPolicyValidation{}, time.Time{}, status.Errorf(codes.PermissionDenied, "selected destination %s/%s is eligible but not preferred by current placement policy", req.ClusterId, req.NodeId)
		}
		result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED
		result.Choice = placement.Choice{ClusterID: req.ClusterId, NodeID: req.NodeId, GroupID: assessment.GroupID, DistanceKM: assessment.DistanceKM}
	case placement.CapacityFull:
		result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED
	case placement.NodeUnavailable:
		result.Outcome = placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE
	case placement.StaleTelemetry, placement.UnknownCapacity, placement.InvalidMetrics, placement.NoSourcePath,
		placement.PolicyFactsUnavailable, placement.UnknownOwnership, placement.PriceUnavailable:
		return PlacementPolicyValidation{}, time.Time{}, status.Errorf(codes.Unavailable, "selected destination %s/%s cannot be assessed (%s)", req.ClusterId, req.NodeId, assessment.Reason)
	default:
		return PlacementPolicyValidation{}, time.Time{}, status.Errorf(codes.PermissionDenied, "selected destination %s/%s has no current placement permission (%s)", req.ClusterId, req.NodeId, assessment.Reason)
	}
	return result, expiry, nil
}

func (gate *PlacementPolicyGate) finish(ctx context.Context, result PlacementPolicyValidation) (PlacementPolicyValidation, error) {
	if contextErr := ctx.Err(); contextErr != nil {
		return PlacementPolicyValidation{}, status.FromContextError(contextErr).Err()
	}
	if !gate.now().Before(result.ExpiresAt) {
		return PlacementPolicyValidation{}, status.Error(codes.FailedPrecondition, "placement revalidation expired")
	}
	return result, nil
}

func placementVerb(query *placementpb.CandidateQuery) placement.Verb {
	if query.GetVerb() == placementpb.Verb_VERB_INGEST {
		return placement.Ingest
	}
	return placement.Serve
}

func (gate *PlacementPolicyGate) readPair(ctx context.Context, req *placementpb.PreparePlacementRequest) (localauthority.PlacementPair, error) {
	ctx, cancel := context.WithTimeout(ctx, localauthority.PlacementReadTimeout)
	defer cancel()
	query := req.GetQuery()
	pair, err := gate.Authority.Placement(ctx, query.TenantId, query.ObjectId, query.InternalName)
	if contextErr := ctx.Err(); contextErr != nil {
		return localauthority.PlacementPair{}, status.FromContextError(contextErr).Err()
	}
	return pair, err
}

// errPlacementAuthorityMismatch marks a read whose authority no longer
// matches the request's policy revisions or grants. Admission, which built the
// request from its own read, retries it on the newer snapshot.
var errPlacementAuthorityMismatch = status.Error(codes.FailedPrecondition, "placement authority changed or belongs to another cell")

func (gate *PlacementPolicyGate) readAuthority(ctx context.Context, req *placementpb.PreparePlacementRequest) (balancer.PlacementAuthority, error) {
	pair, err := gate.readPair(ctx, req)
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	authority, err := balancer.CompilePlacementAuthority(pair, placementVerb(req.GetQuery()), gate.now())
	if err != nil {
		return balancer.PlacementAuthority{}, err
	}
	if !authority.MatchesQuery(req.GetQuery(), gate.CellID) {
		return balancer.PlacementAuthority{}, errPlacementAuthorityMismatch
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
