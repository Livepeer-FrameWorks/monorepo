package federation

import (
	"context"
	"errors"
	"slices"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (runtime *LivePushPreparationRuntime) RetainPreparedSourceDemand(ctx context.Context, request *placementpb.PreparePlacementRequest, receipt PlacementReceipt) error {
	if receipt.Pull == nil || receipt.Response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED {
		return nil
	}
	if runtime == nil || runtime.Registry == nil || request.GetQuery().GetVerb() != placementpb.Verb_VERB_SERVE || !validPlacementPullBinding(request, receipt.Pull) {
		return ErrPlacementReceiptConflict
	}
	demand := &placementpb.CandidateQuery{TenantId: request.Query.TenantId, ObjectId: request.Query.ObjectId,
		InternalName: request.Query.InternalName, Verb: request.Query.Verb, Protocol: request.Query.Protocol,
		SourceGeneration: request.Query.SourceGeneration, ClientLocation: proto.CloneOf(request.Query.ClientLocation)}
	payload, err := protojson.Marshal(demand)
	if err != nil {
		return err
	}
	pull := receipt.Pull
	return runtime.Registry.RecordInboundPlacementDemand(ctx, request.Query.InternalName, control.InboundPull{
		TenantID: request.Query.TenantId, AttemptID: pull.AttemptID, DestClusterID: request.ClusterId, DestNodeID: request.NodeId,
		SourceClusterID: pull.SourceCellID, SourceMediaClusterID: pull.SourceClusterID, SourceNodeID: pull.SourceNodeID,
		SourceGeneration: pull.SourceGeneration, SourceRevision: pull.SourceRevision,
	}, string(payload))
}

// ResolveOrReauthorizeSource uses completed evidence when available. Missing or
// expired evidence requires a new exact-node preparation using retained viewer
// context and current authority; storage failures never become a renewal request.
func (destination *PlacementDestination) ResolveOrReauthorizeSource(ctx context.Context, runtime *LivePushPreparationRuntime, input PlacementSourceIdentity) (PreparedPlacementSource, error) {
	if destination == nil || runtime == nil || destination.RetainPrepared == nil {
		return PreparedPlacementSource{}, status.Error(codes.Unavailable, "source reauthorization is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runtime.ResolvePreparedSource(ctx, destination.Receipts, input)
	if err == nil || (!errors.Is(err, ErrPlacementReceiptMissing) && !errors.Is(err, ErrPlacementReceiptExpired)) {
		return result, err
	}
	pull, found, err := runtime.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if !found || pull.TenantID != input.TenantID || pull.DestClusterID != input.ClusterID || pull.PlacementDemand == "" || len(pull.PlacementDemand) > 65536 {
		return PreparedPlacementSource{}, status.Error(codes.FailedPrecondition, "source has no retained placement demand")
	}
	query := &placementpb.CandidateQuery{}
	if err = protojson.Unmarshal([]byte(pull.PlacementDemand), query); err != nil {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	if query.TenantId != input.TenantID || query.ObjectId != input.ObjectID || query.InternalName != input.InternalName || query.Verb != placementpb.Verb_VERB_SERVE || query.SourceGeneration != pull.SourceGeneration {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	authority, err := runtime.sourceAuthority(ctx, input)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	query.PolicyDigest, query.PolicyRevision, query.ParentRevision = authority.PolicyDigest, authority.PolicyRevision, authority.ParentRevision
	query.ClusterIds = nil
	for _, cell := range authority.Cells {
		if cell.ID == runtime.Paths.CellID {
			query.ClusterIds = slices.Clone(cell.ClusterIDs)
			break
		}
	}
	if !slices.Contains(query.ClusterIds, input.ClusterID) {
		return PreparedPlacementSource{}, status.Error(codes.PermissionDenied, "source destination is no longer granted")
	}
	fence, err := runtime.currentDestinationFence(ctx, input.NodeID, input.ClusterID)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	if fence != input.DestinationFence {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	now := destination.now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		return PreparedPlacementSource{}, err
	}
	until := minPlacementExpiry(now.Truncate(time.Millisecond).Add(placement.PreparationLifetime), authority.ExpiresAt)
	if _, err = destination.PreparePlacement(ctx, &placementpb.PreparePlacementRequest{
		Query: query, ClusterId: input.ClusterID, NodeId: input.NodeID, AttemptId: attempt, ExpiresAt: timestamppb.New(until),
	}); err != nil {
		return PreparedPlacementSource{}, err
	}
	result, err = runtime.ResolvePreparedSource(ctx, destination.Receipts, input)
	if err == nil && (result.AttemptID != pull.AttemptID || result.DTSCURL != pull.DTSCURL) {
		return PreparedPlacementSource{}, ErrPlacementReceiptConflict
	}
	return result, err
}
