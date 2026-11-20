package grpc

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func nodeCapacityFromRecord(row quartermasterdb.GetNodeCapacityRow) *quartermasterpb.NodeCapacity {
	result := &quartermasterpb.NodeCapacity{
		EffectiveEgressBytesPerSec: capacity.DefaultBudget, EffectiveIngressBytesPerSec: capacity.DefaultBudget,
		EgressSource:  quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT,
		IngressSource: quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT,
		Revision:      uint64(row.Revision.Int64), UpdatedBy: row.UpdatedBy.String,
	}
	if row.EgressBudgetBytesPerSec.Valid {
		budget := uint64(row.EgressBudgetBytesPerSec.Int64)
		result.EgressBudgetBytesPerSec = &budget
		result.EffectiveEgressBytesPerSec = budget
		result.EgressSource = quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR
	}
	if row.IngressBudgetBytesPerSec.Valid {
		budget := uint64(row.IngressBudgetBytesPerSec.Int64)
		result.IngressBudgetBytesPerSec = &budget
		result.EffectiveIngressBytesPerSec = budget
		result.IngressSource = quartermasterpb.CapacitySource_CAPACITY_SOURCE_OPERATOR
	}
	if row.UpdatedAt.Valid {
		result.UpdatedAt = timestamppb.New(row.UpdatedAt.Time)
	}
	return result
}

func capacityPrivileged(ctx context.Context) bool {
	return ctxkeys.GetAuthType(ctx) == "service" || ctxkeys.IsPlatformOperator(ctx)
}

func capacityLookupError(ctx context.Context, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		if !capacityPrivileged(ctx) {
			return status.Error(codes.PermissionDenied, privateInfrastructureDenied)
		}
		return status.Error(codes.NotFound, "node not found")
	}
	return status.Errorf(codes.Internal, "read node capacity: %v", err)
}

func (s *QuartermasterServer) GetNodeCapacity(ctx context.Context, req *quartermasterpb.GetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	if err := requireTenantResourceActor(ctx, ctxkeys.GetTenantID(ctx), authz.ActionReadPrivateInfrastructure); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	if req.ExpectedOwnerTenantId != nil {
		var result *quartermasterpb.NodeCapacity
		err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			q := quartermasterdb.New(tx)
			owner, err := q.LockNodeCapacityOwner(ctx, strings.TrimSpace(req.GetNodeId()))
			if err != nil {
				return capacityLookupError(ctx, err)
			}
			if authErr := requireOwnedResourceActor(ctx, owner.OwnerTenantID, authz.ActionReadPrivateInfrastructure); authErr != nil {
				return authErr
			}
			if owner.OwnerTenantID.String != req.GetExpectedOwnerTenantId() {
				return status.Error(codes.Aborted, "node ownership changed; read the node and retry")
			}
			row, err := q.GetNodeCapacity(ctx, quartermasterdb.GetNodeCapacityParams{NodeID: owner.NodeID, Privileged: capacityPrivileged(ctx), TenantID: ctxkeys.GetTenantID(ctx)})
			if err != nil {
				return capacityLookupError(ctx, err)
			}
			result = nodeCapacityFromRecord(row)
			return nil
		})
		if err != nil {
			return nil, retryableTxStatus(err, "read node capacity")
		}
		return result, nil
	}
	q := quartermasterdb.New(s.db)
	node, err := q.GetInfrastructureNode(ctx, req.GetNodeId())
	if err != nil {
		return nil, capacityLookupError(ctx, err)
	}
	if authErr := requireOwnedResourceActor(ctx, sql.NullString{String: node.OwnerTenantID, Valid: node.OwnerTenantID != ""}, authz.ActionReadPrivateInfrastructure); authErr != nil {
		return nil, authErr
	}
	row, err := q.GetNodeCapacity(ctx, quartermasterdb.GetNodeCapacityParams{
		NodeID: node.NodeID, Privileged: capacityPrivileged(ctx), TenantID: ctxkeys.GetTenantID(ctx),
	})
	if err != nil {
		return nil, capacityLookupError(ctx, err)
	}
	return nodeCapacityFromRecord(row), nil
}

func (s *QuartermasterServer) SetNodeCapacity(ctx context.Context, req *quartermasterpb.SetNodeCapacityRequest) (*quartermasterpb.NodeCapacity, error) {
	if err := requireTenantResourceActor(ctx, ctxkeys.GetTenantID(ctx), authz.ActionManageEdgeCluster); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetNodeId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id is required")
	}
	for _, budget := range []*uint64{req.EgressBudgetBytesPerSec, req.IngressBudgetBytesPerSec} {
		if budget != nil && (*budget == 0 || *budget > math.MaxInt64) {
			return nil, status.Error(codes.InvalidArgument, "bandwidth budgets must be positive bytes per second within signed 64-bit range")
		}
	}
	if req.GetExpectedRevision() >= math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "expected_revision is out of range")
	}
	actor := ctxkeys.GetUserID(ctx)
	if ctxkeys.GetAuthType(ctx) == "service" && req.GetActor() != nil {
		actor = req.GetActor().GetUserId()
	}
	if actor == "" {
		actor = ctxkeys.GetAuthType(ctx)
	}
	nullBudget := func(value *uint64) sql.NullInt64 {
		if value == nil {
			return sql.NullInt64{}
		}
		return sql.NullInt64{Int64: int64(*value), Valid: true}
	}
	var result *quartermasterpb.NodeCapacity
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := quartermasterdb.New(tx)
		// Ownership cannot change between authorization and the capacity write.
		owner, err := q.LockNodeCapacityOwner(ctx, strings.TrimSpace(req.GetNodeId()))
		if err != nil {
			return capacityLookupError(ctx, err)
		}
		if authErr := requireOwnedResourceActor(ctx, owner.OwnerTenantID, authz.ActionManageEdgeCluster); authErr != nil {
			return authErr
		}
		if req.ExpectedOwnerTenantId != nil && owner.OwnerTenantID.String != req.GetExpectedOwnerTenantId() {
			return status.Error(codes.Aborted, "node ownership changed; read the node and retry")
		}
		rows, err := q.UpsertNodeCapacity(ctx, quartermasterdb.UpsertNodeCapacityParams{
			NodeID: owner.NodeID, EgressBudgetBytesPerSec: nullBudget(req.EgressBudgetBytesPerSec),
			IngressBudgetBytesPerSec: nullBudget(req.IngressBudgetBytesPerSec), ExpectedRevision: int64(req.GetExpectedRevision()),
			UpdatedBy: actor, TenantID: ctxkeys.GetTenantID(ctx), Privileged: capacityPrivileged(ctx),
		})
		if err != nil {
			return err
		}
		if rows != 1 {
			return status.Error(codes.Aborted, "node capacity revision changed; read capacity and retry")
		}
		row, err := q.GetNodeCapacity(ctx, quartermasterdb.GetNodeCapacityParams{
			NodeID: owner.NodeID, TenantID: ctxkeys.GetTenantID(ctx), Privileged: capacityPrivileged(ctx),
		})
		if err != nil {
			return err
		}
		result = nodeCapacityFromRecord(row)
		after, err := structpb.NewStruct(map[string]any{
			"egress_budget_bytes_per_sec":  strconv.FormatUint(result.GetEffectiveEgressBytesPerSec(), 10),
			"ingress_budget_bytes_per_sec": strconv.FormatUint(result.GetEffectiveIngressBytesPerSec(), 10),
			"egress_source":                result.GetEgressSource().String(), "ingress_source": result.GetIngressSource().String(),
			"revision": strconv.FormatUint(result.GetRevision(), 10),
		})
		if err != nil {
			return err
		}
		_, err = s.EnqueueServiceEventTx(ctx, tx, &ipcpb.ServiceEvent{
			EventType: serviceevents.NodeCapacityUpdated, Source: "quartermaster", Timestamp: timestamppb.Now(),
			UserId: actor, ResourceType: "node", ResourceId: owner.NodeID,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: &ipcpb.ClusterEvent{
				ClusterId: owner.ClusterID, TenantId: owner.OwnerTenantID.String, AfterState: after,
			}},
		})
		return err
	})
	if err != nil {
		return nil, retryableTxStatus(err, "set node capacity")
	}
	return result, nil
}

func (s *QuartermasterServer) ListNodeCapacities(ctx context.Context, req *quartermasterpb.ListNodeCapacitiesRequest) (*quartermasterpb.ListNodeCapacitiesResponse, error) {
	if err := requireTenantResourceActor(ctx, ctxkeys.GetTenantID(ctx), authz.ActionReadPrivateInfrastructure); err != nil {
		return nil, err
	}
	if len(req.GetNodeIds())+len(req.GetClusterIds()) == 0 || len(req.GetNodeIds())+len(req.GetClusterIds()) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "provide between 1 and 1000 node_ids or cluster_ids")
	}
	q := quartermasterdb.New(s.db)
	for _, clusterID := range req.GetClusterIds() {
		if _, err := s.requireClusterLifecycleActor(ctx, clusterID, authz.ActionReadPrivateInfrastructure); err != nil {
			return nil, err
		}
	}
	result := &quartermasterpb.ListNodeCapacitiesResponse{Capacities: make(map[string]*quartermasterpb.NodeCapacity)}
	if len(req.GetNodeIds()) > 0 {
		rows, err := q.ListNodeCapacitiesByNodes(ctx, quartermasterdb.ListNodeCapacitiesByNodesParams{
			NodeIds: req.GetNodeIds(), TenantID: ctxkeys.GetTenantID(ctx), Privileged: capacityPrivileged(ctx),
		})
		if err != nil {
			return nil, capacityLookupError(ctx, err)
		}
		for _, row := range rows {
			result.Capacities[row.NodeID] = nodeCapacityFromRecord(quartermasterdb.GetNodeCapacityRow(row))
		}
		if !capacityPrivileged(ctx) {
			for _, nodeID := range req.GetNodeIds() {
				if _, ok := result.Capacities[nodeID]; !ok {
					return nil, status.Error(codes.PermissionDenied, privateInfrastructureDenied)
				}
			}
		}
	}
	if len(req.GetClusterIds()) > 0 {
		rows, err := q.ListNodeCapacitiesByClusters(ctx, quartermasterdb.ListNodeCapacitiesByClustersParams{
			ClusterIds: req.GetClusterIds(), TenantID: ctxkeys.GetTenantID(ctx), Privileged: capacityPrivileged(ctx),
		})
		if err != nil {
			return nil, capacityLookupError(ctx, err)
		}
		for _, row := range rows {
			result.Capacities[row.NodeID] = nodeCapacityFromRecord(quartermasterdb.GetNodeCapacityRow(row))
		}
	}
	return result, nil
}
