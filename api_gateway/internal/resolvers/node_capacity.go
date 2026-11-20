package resolvers

import (
	"context"
	"fmt"
	"math"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/loaders"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func capacityInputBytes(value *float64, field string) (*uint64, *model.ValidationError) {
	if value == nil {
		return nil, nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 1 || *value > 9007199254740991 || math.Trunc(*value) != *value {
		return nil, &model.ValidationError{Message: "budget must be positive whole bytes per second within the exact integer range", Field: &field}
	}
	bytes := uint64(*value)
	return &bytes, nil
}

func (r *Resolver) DoSetNodeCapacity(ctx context.Context, input model.SetNodeCapacityInput) (model.SetNodeCapacityResult, error) {
	if err := middleware.RequireTenantAction(ctx, "infrastructure:write", authz.ActionManageEdgeCluster, tenantIDFromContext(ctx)); err != nil {
		return nil, err
	}
	nodeID, validationErr := normalizeNodeModeID(input.NodeID)
	if validationErr != nil {
		return validationErr, nil
	}
	if input.ExpectedRevision < 0 {
		return &model.ValidationError{Message: "expectedRevision must be nonnegative", Field: strPtr("expectedRevision")}, nil
	}
	egress, validationErr := capacityInputBytes(input.EgressBudgetBytesPerSec, "egressBudgetBytesPerSec")
	if validationErr != nil {
		return validationErr, nil
	}
	ingress, validationErr := capacityInputBytes(input.IngressBudgetBytesPerSec, "ingressBudgetBytesPerSec")
	if validationErr != nil {
		return validationErr, nil
	}
	if middleware.IsDemoMode(ctx) {
		record := &quartermasterpb.NodeCapacity{EffectiveEgressBytesPerSec: capacity.DefaultBudget, EffectiveIngressBytesPerSec: capacity.DefaultBudget, Revision: uint64(input.ExpectedRevision) + 1, UpdatedAt: timestamppb.Now(), EgressBudgetBytesPerSec: egress, IngressBudgetBytesPerSec: ingress}
		if egress != nil {
			record.EffectiveEgressBytesPerSec = *egress
			record.EgressSource = quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR
		}
		if ingress != nil {
			record.EffectiveIngressBytesPerSec = *ingress
			record.IngressSource = quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR
		}
		return &quartermasterpb.InfrastructureNode{NodeId: nodeID, Capacity: record}, nil
	}
	result, err := r.Clients.Commodore.SetNodeCapacity(ctx, &quartermasterpb.SetNodeCapacityRequest{NodeId: nodeID, EgressBudgetBytesPerSec: egress, IngressBudgetBytesPerSec: ingress, ExpectedRevision: uint64(input.ExpectedRevision)})
	if err != nil {
		if v := mapInvalidArgument(err); v != nil {
			return v, nil
		}
		if v := mapNotFound(err); v != nil {
			return v, nil
		}
		if status.Code(err) == codes.Aborted {
			return &model.ValidationError{Message: "Node capacity changed; refresh the node and retry", Field: strPtr("expectedRevision")}, nil
		}
		return nil, fmt.Errorf("set node capacity: %w", err)
	}
	return &quartermasterpb.InfrastructureNode{NodeId: nodeID, Capacity: result}, nil
}

func nodeCapacityModel(record *quartermasterpb.NodeCapacity) (*model.NodeCapacity, error) {
	if record == nil {
		return nil, nil
	}
	if record.GetRevision() > math.MaxInt32 {
		return nil, fmt.Errorf("node capacity revision exceeds GraphQL Int range")
	}
	egress, ingress := record.GetEffectiveEgressBytesPerSec(), record.GetEffectiveIngressBytesPerSec()
	if egress == 0 {
		egress = capacity.DefaultBudget
	}
	if ingress == 0 {
		ingress = capacity.DefaultBudget
	}
	egressSource, ingressSource := model.CapacityBudgetSourceDefault, model.CapacityBudgetSourceDefault
	if record.GetEgressSource() == quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR {
		egressSource = model.CapacityBudgetSourceOperator
	}
	if record.GetIngressSource() == quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR {
		ingressSource = model.CapacityBudgetSourceOperator
	}
	return &model.NodeCapacity{EgressBudgetBytesPerSec: float64(egress), IngressBudgetBytesPerSec: float64(ingress), EgressSource: egressSource, IngressSource: ingressSource, Revision: int(record.GetRevision()), UpdatedAt: protoTimePtr(record.GetUpdatedAt())}, nil
}

func (r *Resolver) DoNodeCapacity(ctx context.Context, node *quartermasterpb.InfrastructureNode) (*model.NodeCapacity, error) {
	if err := middleware.RequireTenantAction(ctx, "infrastructure:read", authz.ActionReadPrivateInfrastructure, tenantIDFromContext(ctx)); err != nil {
		return nil, err
	}
	record := node.GetCapacity()
	if middleware.IsDemoMode(ctx) && record == nil {
		record = &quartermasterpb.NodeCapacity{EffectiveEgressBytesPerSec: capacity.DefaultBudget, EffectiveIngressBytesPerSec: capacity.DefaultBudget}
	}
	result, err := nodeCapacityModel(record)
	if err != nil || result == nil {
		return result, err
	}
	if middleware.IsDemoMode(ctx) {
		applied := result.Revision
		result.AppliedRevision = &applied
		return result, nil
	}
	if nodeSkipsOperationalMode(node) {
		return result, nil
	}
	health, err := r.nodeHealthFor(ctx, node.GetNodeId())
	if err != nil {
		if status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.Unauthenticated {
			return nil, err
		}
		return result, nil
	}
	if health != nil && health.CapacityRevisionApplied != nil && health.GetCapacityRevisionApplied() <= math.MaxInt32 {
		applied := int(health.GetCapacityRevisionApplied())
		result.AppliedRevision = &applied
	}
	return result, nil
}

// clusterCapacityRecords deduplicates reads for capacity and its deprecated alias.
func (r *Resolver) clusterCapacityRecords(ctx context.Context, clusterID string) (map[string]*quartermasterpb.NodeCapacity, error) {
	load := func() (any, error) {
		response, err := r.Clients.Quartermaster.ListNodeCapacities(ctx, &quartermasterpb.ListNodeCapacitiesRequest{ClusterIds: []string{clusterID}})
		if err != nil {
			return nil, err
		}
		return response.GetCapacities(), nil
	}
	var value any
	var err error
	if l := loaders.FromContext(ctx); l != nil {
		value, err = l.Memo.GetOrLoad("cluster-capacity:"+clusterID, load)
	} else {
		value, err = load()
	}
	if err != nil {
		return nil, err
	}
	records, ok := value.(map[string]*quartermasterpb.NodeCapacity)
	if !ok {
		return nil, fmt.Errorf("unexpected capacity cache value %T", value)
	}
	return records, nil
}

func (r *Resolver) DoClusterCapacity(ctx context.Context, cluster *quartermasterpb.InfrastructureCluster) (*model.ClusterCapacity, error) {
	var records map[string]*quartermasterpb.NodeCapacity
	if middleware.IsDemoMode(ctx) {
		records = make(map[string]*quartermasterpb.NodeCapacity)
		for _, node := range demo.GenerateInfrastructureNodes() {
			if node.GetClusterId() == cluster.GetClusterId() {
				records[node.GetNodeId()] = &quartermasterpb.NodeCapacity{EffectiveEgressBytesPerSec: capacity.DefaultBudget, EffectiveIngressBytesPerSec: capacity.DefaultBudget}
			}
		}
	} else {
		if err := middleware.RequireTenantAction(ctx, "infrastructure:read", authz.ActionReadPrivateInfrastructure, tenantIDFromContext(ctx)); err != nil {
			return nil, err
		}
		if r.Clients == nil || r.Clients.Quartermaster == nil {
			return nil, nil
		}
		var err error
		records, err = r.clusterCapacityRecords(ctx, cluster.GetClusterId())
		if err != nil {
			if status.Code(err) == codes.Unauthenticated {
				return nil, err
			}
			return nil, nil
		}
	}
	result := &model.ClusterCapacity{}
	for _, record := range records {
		node, err := nodeCapacityModel(record)
		if err != nil {
			return nil, err
		}
		if node == nil {
			continue
		}
		result.EgressBudgetBytesPerSec += node.EgressBudgetBytesPerSec
		result.IngressBudgetBytesPerSec += node.IngressBudgetBytesPerSec
		egress := record.GetEffectiveEgressBytesPerSec()
		if egress == 0 {
			egress = capacity.DefaultBudget
		}
		result.EgressUsableBytesPerSec += float64((capacity.Link{Budget: egress}).Usable())
		ingress := record.GetEffectiveIngressBytesPerSec()
		if ingress == 0 {
			ingress = capacity.DefaultBudget
		}
		result.IngressUsableBytesPerSec += float64((capacity.Link{Budget: ingress}).Usable())
		if node.EgressSource == model.CapacityBudgetSourceDefault || node.IngressSource == model.CapacityBudgetSourceDefault {
			result.DefaultBudgetNodes++
		}
	}
	return result, nil
}

func (r *Resolver) DoClusterMaxBandwidthMbps(ctx context.Context, cluster *quartermasterpb.InfrastructureCluster) (int, error) {
	result, err := r.DoClusterCapacity(ctx, cluster)
	if err != nil {
		return 0, err
	}
	if result == nil {
		// Cluster metadata remains readable without access to private node inventory.
		return 0, nil
	}
	mbps := result.EgressBudgetBytesPerSec / 125000
	if mbps > math.MaxInt32 {
		return 0, fmt.Errorf("cluster bandwidth exceeds GraphQL Int range; use capacity")
	}
	return int(mbps), nil
}
