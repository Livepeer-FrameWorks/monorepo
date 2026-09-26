package federation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PlacementRPC is implemented by a destination service locally and by the
// authenticated federation client remotely. Both paths use identical DTOs.
type PlacementRPC interface {
	QueryPlacementCandidates(context.Context, *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error)
	PreparePlacement(context.Context, *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error)
}

// PlacementTransport resolves cells from authorized topology. The caller must not
// source addresses or cell membership from public request parameters.
type PlacementTransport struct {
	LocalCellID  string
	Local        PlacementRPC
	Client       *FederationClient
	CellAddress  func(string) string
	Observations *PlacementObservationCache
	Logger       logging.Logger
}

func (transport PlacementTransport) Router() balancer.PlacementRouter {
	return balancer.PlacementRouter{Observe: transport.Observe, Prepare: transport.Prepare, Logger: transport.Logger}
}

func (transport PlacementTransport) Observe(ctx context.Context, cell balancer.PlacementCell, req balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
	query := placementQuery(cell, req)
	if err := placement.ValidateCandidateQuery(query); err != nil {
		return balancer.PlacementCellObservation{}, err
	}
	if transport.Observations == nil || cell.ID == transport.LocalCellID || req.Verb != placement.Serve || req.TenantAuthorityVersion <= 0 || req.ObjectAuthorityVersion <= 0 {
		return transport.observe(ctx, cell, req, query)
	}
	address := transport.address(cell.ID)
	if transport.Client == nil || address == "" {
		return balancer.PlacementCellObservation{}, errors.New("placement destination cell unavailable")
	}
	// Discovery is unranked. Client geography belongs to the local evaluation,
	// not to shared node facts or the peer request supplying those facts.
	query.ClientLocation = nil
	cell.ClusterIDs = slices.Clone(cell.ClusterIDs)
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(query)
	if err != nil {
		return balancer.PlacementCellObservation{}, err
	}
	key := placementObservationKey{CellID: cell.ID, Address: address, Query: string(encoded), TenantVersion: req.TenantAuthorityVersion, ObjectVersion: req.ObjectAuthorityVersion}
	return transport.Observations.observe(ctx, key, func(sharedCtx context.Context) (balancer.PlacementCellObservation, error) {
		// Pin the authenticated peer address used in the cache key for this read.
		pinned := transport
		pinned.CellAddress = func(string) string { return address }
		return pinned.observe(sharedCtx, cell, req, query)
	})
}

func (transport PlacementTransport) observe(ctx context.Context, cell balancer.PlacementCell, req balancer.PlacementRouteRequest, query *placementpb.CandidateQuery) (balancer.PlacementCellObservation, error) {
	var response *placementpb.CandidateObservation
	var err error
	if cell.ID == transport.LocalCellID && transport.Local != nil {
		response, err = transport.Local.QueryPlacementCandidates(ctx, query)
	} else {
		addr := transport.address(cell.ID)
		if transport.Client == nil || addr == "" {
			return balancer.PlacementCellObservation{}, fmt.Errorf("placement destination cell %q has no federation address", cell.ID)
		}
		response, err = transport.Client.QueryPlacementCandidates(ctx, cell.ID, addr, query)
	}
	if err != nil {
		return balancer.PlacementCellObservation{}, err
	}
	if mismatch := placementResponseMismatch(response, req); mismatch != "" {
		return balancer.PlacementCellObservation{}, fmt.Errorf("%w: cell %q %s", balancer.ErrPlacementObservation, cell.ID, mismatch)
	}
	result := balancer.PlacementCellObservation{Complete: response.GetComplete(), ObservedAt: response.GetObservedAt().AsTime(), ExpiresAt: response.GetExpiresAt().AsTime()}
	for _, observed := range response.GetCandidates() {
		candidate, decodeErr := placement.CandidateFromProto(observed, req.Verb)
		if decodeErr != nil {
			return balancer.PlacementCellObservation{}, fmt.Errorf("%w: cell %q candidate %s/%s: %w", balancer.ErrPlacementObservation, cell.ID, observed.GetClusterId(), observed.GetNodeId(), decodeErr)
		}
		if candidate.TenantID != req.TenantID || !slices.Contains(cell.ClusterIDs, candidate.ClusterID) {
			return balancer.PlacementCellObservation{}, fmt.Errorf("%w: cell %q returned candidate %s/%s outside tenant %q or requested clusters", balancer.ErrPlacementObservation, cell.ID, candidate.ClusterID, candidate.NodeID, req.TenantID)
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result, nil
}

// placementResponseMismatch names the first field of a discovery response that
// does not match the route it answers, or returns "".
func placementResponseMismatch(response *placementpb.CandidateObservation, req balancer.PlacementRouteRequest) string {
	switch {
	case response == nil:
		return "returned no observation"
	case response.GetPolicyDigest() != req.PolicyDigest:
		return "policy digest differs"
	case response.GetPolicyRevision() != req.PolicyRevision:
		return fmt.Sprintf("policy revision %d, route %d", response.GetPolicyRevision(), req.PolicyRevision)
	case response.GetParentRevision() != req.ParentRevision:
		return fmt.Sprintf("parent revision %d, route %d", response.GetParentRevision(), req.ParentRevision)
	case response.GetSourceGeneration() != req.SourceGeneration:
		return fmt.Sprintf("source generation %q, route %q", response.GetSourceGeneration(), req.SourceGeneration)
	case len(response.GetCandidates()) > 4096:
		return fmt.Sprintf("%d candidates exceed the bound", len(response.GetCandidates()))
	case response.GetObservedAt() == nil || !response.GetObservedAt().IsValid():
		return "observation time missing or invalid"
	case response.GetExpiresAt() == nil || !response.GetExpiresAt().IsValid():
		return "expiry missing or invalid"
	}
	return ""
}

func (transport PlacementTransport) Prepare(ctx context.Context, cell balancer.PlacementCell, req balancer.PlacementPreparationRequest) (balancer.PlacementPreparationResult, error) {
	query := &placementpb.PreparePlacementRequest{Query: placementQuery(cell, req.Route), ClusterId: req.Choice.ClusterID, NodeId: req.Choice.NodeID, AttemptId: req.AttemptID, ExpiresAt: timestamppb.New(req.ExpiresAt)}
	if err := placement.ValidatePreparationDeadline(query, time.Now()); err != nil {
		return balancer.PlacementPreparationResult{}, balancer.ErrPlacementPreparation
	}
	ctx, cancel := context.WithDeadline(ctx, req.ExpiresAt)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	var response *placementpb.Preparation
	var err error
	if cell.ID == transport.LocalCellID && transport.Local != nil {
		response, err = transport.Local.PreparePlacement(ctx, query)
	} else {
		addr := transport.address(cell.ID)
		if transport.Client == nil || addr == "" {
			return balancer.PlacementPreparationResult{}, errors.New("placement destination cell unavailable")
		}
		response, err = transport.Client.PreparePlacement(ctx, cell.ID, addr, query)
		if err != nil && transport.Logger != nil {
			transport.Logger.WithError(err).WithFields(logging.Fields{
				"cell_id": cell.ID, "peer_addr": addr, "cluster_id": req.Choice.ClusterID, "node_id": req.Choice.NodeID,
				"internal_name": req.Route.InternalName,
			}).Warn("Remote placement preparation failed")
		}
	}
	if err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return balancer.PlacementPreparationResult{}, err
	}
	if err := placement.ValidatePreparationResponse(query, response, time.Now()); err != nil {
		return balancer.PlacementPreparationResult{}, balancer.ErrPlacementPreparation
	}
	var outcome balancer.PlacementPreparationOutcome
	switch response.GetOutcome() {
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED:
		outcome = balancer.PlacementAccepted
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED:
		outcome = balancer.PlacementFull
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE:
		outcome = balancer.PlacementNodeUnavailable
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_SOURCE_UNAVAILABLE:
		outcome = balancer.PlacementSourceUnavailable
	default:
		return balancer.PlacementPreparationResult{}, balancer.ErrPlacementPreparation
	}
	return balancer.PlacementPreparationResult{
		Outcome: outcome, TenantID: response.GetTenantId(), ObjectID: response.GetObjectId(), SourceGeneration: response.GetSourceGeneration(),
		ClusterID: response.GetClusterId(), NodeID: response.GetNodeId(), Protocol: response.GetProtocol(), PolicyRevision: response.GetPolicyRevision(),
		ParentRevision: response.GetParentRevision(), PolicyDigest: response.GetPolicyDigest(), AttemptID: response.GetAttemptId(),
		ExpiresAt: response.GetExpiresAt().AsTime(), Endpoint: response.GetEndpoint(), PublicBaseURL: response.GetPublicBaseUrl(), OutputsJSON: response.GetOutputsJson(), Ready: response.GetReady(),
	}, nil
}

func (transport PlacementTransport) address(cellID string) string {
	if transport.CellAddress == nil {
		return ""
	}
	return transport.CellAddress(cellID)
}

func placementQuery(cell balancer.PlacementCell, req balancer.PlacementRouteRequest) *placementpb.CandidateQuery {
	verb := placementpb.Verb_VERB_UNSPECIFIED
	if req.Verb == placement.Ingest {
		verb = placementpb.Verb_VERB_INGEST
	}
	if req.Verb == placement.Serve {
		verb = placementpb.Verb_VERB_SERVE
	}
	result := &placementpb.CandidateQuery{
		TenantId: req.TenantID, ObjectId: req.ObjectID, InternalName: req.InternalName, Verb: verb, Protocol: req.Protocol,
		ClusterIds: slices.Clone(cell.ClusterIDs), PolicyRevision: req.PolicyRevision, ParentRevision: req.ParentRevision,
		PolicyDigest: req.PolicyDigest, SourceGeneration: req.SourceGeneration,
	}
	if req.Location != nil {
		result.ClientLocation = &placementpb.Coordinates{Latitude: req.Location.Latitude, Longitude: req.Location.Longitude}
	}
	return result
}
