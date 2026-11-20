package grpc

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// SetNodeCapacity commits the operator's budget before attempting live delivery.
// Disconnected nodes receive the persisted budget on their next seed or sync.
func (s *CommodoreServer) SetNodeCapacity(ctx context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	if _, _, err := extractUserContext(ctx); err != nil {
		return nil, err
	}
	owner, err := s.authorizeNodeAction(ctx, req.GetNodeId(), "infrastructure:write", authz.ActionManageEdgeCluster)
	if err != nil {
		return nil, err
	}
	forwarded := &quartermasterpb.SetNodeCapacityRequest{
		NodeId: req.GetNodeId(), EgressBudgetBytesPerSec: req.EgressBudgetBytesPerSec,
		IngressBudgetBytesPerSec: req.IngressBudgetBytesPerSec, ExpectedRevision: req.GetExpectedRevision(),
	}
	forwarded.NodeId = strings.TrimSpace(req.GetNodeId())
	forwarded.Actor = s.requestActor(ctx)
	expectedOwner := owner.GetOwnerTenantId()
	forwarded.ExpectedOwnerTenantId = &expectedOwner
	result, err := s.quartermasterClient.SetNodeCapacity(ctx, forwarded)
	if err != nil {
		return nil, err
	}
	client, pushErr := s.resolveFoghornForNode(owner)
	if pushErr == nil {
		_, pushErr = client.PushNodeCapacity(ctx, forwarded.NodeId)
	}
	if pushErr != nil {
		s.logger.WithError(pushErr).WithField("node_id", forwarded.NodeId).Warn("Node capacity persisted; live delivery pending")
	}
	return result, nil
}

func (s *CommodoreServer) GetNodeCapacity(ctx context.Context, req *quartermasterpb.GetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	if _, _, err := extractUserContext(ctx); err != nil {
		return nil, err
	}
	owner, err := s.authorizeNodeAction(ctx, req.GetNodeId(), "infrastructure:read", authz.ActionReadPrivateInfrastructure)
	if err != nil {
		return nil, err
	}
	return s.quartermasterClient.GetNodeCapacityForOwner(ctx, strings.TrimSpace(req.GetNodeId()), owner.GetOwnerTenantId())
}
