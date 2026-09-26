package federation

import (
	"context"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *FederationServer) QueryPlacementCandidates(ctx context.Context, req *placementpb.CandidateQuery) (*placementpb.CandidateObservation, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if s.federationMutationsDisabled() {
		return nil, status.Error(codes.FailedPrecondition, "federation_mutations_disabled")
	}
	if err := placement.ValidateCandidateQuery(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if s.placement == nil {
		return nil, status.Error(codes.Unavailable, "placement destination is not ready")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := s.placement.QueryPlacementCandidates(ctx, req)
	if err != nil {
		return nil, s.placementRPCError("query", req.GetTenantId(), req.GetInternalName(), "", "", err)
	}
	return response, nil
}

func (s *FederationServer) PreparePlacement(ctx context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
	if err := requireFederationServiceAuth(ctx); err != nil {
		return nil, err
	}
	if s.federationMutationsDisabled() {
		return nil, status.Error(codes.FailedPrecondition, "federation_mutations_disabled")
	}
	if err := placement.ValidatePreparationDeadline(req, time.Now()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if s.placement == nil {
		return nil, status.Error(codes.Unavailable, "placement destination is not ready")
	}
	deadline := time.Now().Add(5 * time.Second)
	if req.GetExpiresAt().AsTime().Before(deadline) {
		deadline = req.GetExpiresAt().AsTime()
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	response, err := s.placement.PreparePlacement(ctx, req)
	if err != nil {
		return nil, s.placementRPCError("prepare", req.GetQuery().GetTenantId(), req.GetQuery().GetInternalName(), req.GetClusterId(), req.GetNodeId(), err)
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := placement.ValidatePreparationResponse(req, response, time.Now()); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "invalid placement acknowledgement")
	}
	return response, nil
}

// placementRPCError returns a status the calling cell can act on. The
// federation interceptor reduces any plain error to an anonymous internal
// error, so a runtime failure is logged here with the request it refused and
// mapped to a status: a source pull that cannot be arranged is retryable.
func (s *FederationServer) placementRPCError(op, tenantID, internalName, clusterID, nodeID string, err error) error {
	if _, isStatus := status.FromError(err); isStatus {
		return err
	}
	if s.logger != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"op": op, "tenant_id": tenantID, "internal_name": internalName, "cluster_id": clusterID, "node_id": nodeID,
		}).Warn("Federation placement request failed")
	}
	if IsArrangeInfraError(err) {
		return status.Error(codes.Unavailable, "source pull cannot be arranged")
	}
	return status.Error(codes.Internal, "placement request failed")
}
