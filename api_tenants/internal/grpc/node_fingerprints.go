package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListNodeFingerprints is a platform-operator read across every tenant. It
// has no tenant filter because the question it answers is global: the unique
// fingerprint indexes span all tenants, so a duplicate can pair bindings of two
// different tenants. The operator JWT check is the only gate.
func (s *QuartermasterServer) ListNodeFingerprints(ctx context.Context, req *quartermasterpb.ListNodeFingerprintsRequest) (*quartermasterpb.ListNodeFingerprintsResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}
	filter := quartermasterdb.NodeFingerprintBindingFilter{
		ClusterID:      strings.TrimSpace(req.GetClusterId()),
		DuplicatesOnly: req.GetDuplicatesOnly(),
		Backward:       params.Direction == pagination.Backward,
		Limit:          params.Limit + 1,
	}
	if params.Cursor != nil {
		if _, parseErr := uuid.Parse(params.Cursor.ID); parseErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid pagination cursor")
		}
		cursorTime := params.Cursor.Timestamp
		filter.CursorTime = &cursorTime
		filter.CursorID = params.Cursor.ID
	}
	rows, total, err := quartermasterdb.New(s.db).ListNodeFingerprintBindingsPage(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	resultsLen := len(rows)
	if resultsLen > params.Limit {
		rows = rows[:params.Limit]
	}
	if params.Direction == pagination.Backward {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	out := make([]*quartermasterpb.NodeFingerprintBinding, 0, len(rows))
	for _, row := range rows {
		binding := &quartermasterpb.NodeFingerprintBinding{
			FingerprintId:            row.ID,
			NodeId:                   row.NodeID,
			TenantId:                 row.TenantID,
			ClusterId:                row.ClusterID,
			FingerprintMachineSha256: row.MachineSHA256,
			FingerprintMacsSha256:    row.MACsSHA256,
			HasIdentityKey:           row.HasIdentityKey,
			MachineDuplicateCount:    row.MachineDuplicates,
			MacsDuplicateCount:       row.MACsDupes,
		}
		if row.FirstSeen.Valid {
			binding.FirstSeen = timestamppb.New(row.FirstSeen.Time)
		}
		if row.LastSeen.Valid {
			binding.LastSeen = timestamppb.New(row.LastSeen.Time)
		}
		out = append(out, binding)
	}
	var startCursor, endCursor string
	if len(rows) > 0 {
		startCursor = pagination.EncodeCursor(rows[0].SortTime, rows[0].ID)
		endCursor = pagination.EncodeCursor(rows[len(rows)-1].SortTime, rows[len(rows)-1].ID)
	}
	return &quartermasterpb.ListNodeFingerprintsResponse{
		Fingerprints: out,
		Pagination:   pagination.BuildResponse(resultsLen, params.Limit, params.Direction, total, startCursor, endCursor),
	}, nil
}

// UnbindNodeFingerprint deletes one binding and records who removed it and
// why, in the same transaction. Like the listing it is cross-tenant: the
// operator removes a stale binding whatever tenant it belongs to, and the
// delete is fenced on (fingerprint_id, node_id) rather than a tenant.
func (s *QuartermasterServer) UnbindNodeFingerprint(ctx context.Context, req *quartermasterpb.UnbindNodeFingerprintRequest) (*quartermasterpb.UnbindNodeFingerprintResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	nodeID := strings.TrimSpace(req.GetNodeId())
	fingerprintID := strings.TrimSpace(req.GetFingerprintId())
	reason := strings.TrimSpace(req.GetReason())
	if nodeID == "" || fingerprintID == "" || reason == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id, fingerprint_id, and reason are required")
	}
	if _, err := uuid.Parse(fingerprintID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "fingerprint_id must be a UUID")
	}
	operatorID := ctxkeys.GetUserID(ctx)

	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		unbound, err := quartermasterdb.New(tx).DeleteNodeFingerprintBinding(ctx, fingerprintID, nodeID)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "no fingerprint binding with that id for that node")
		}
		if err != nil {
			return err
		}
		if ctxkeys.IsDemoMode(ctx) {
			return nil
		}
		event, err := buildNodeFingerprintUnboundEvent(operatorID, nodeID, fingerprintID, reason, unbound)
		if err != nil {
			return err
		}
		_, err = s.EnqueueServiceEventTx(ctx, tx, event)
		return err
	})
	if err != nil {
		return nil, retryableTxStatus(err, "failed to unbind node fingerprint")
	}
	return &quartermasterpb.UnbindNodeFingerprintResponse{NodeId: nodeID, FingerprintId: fingerprintID}, nil
}

// buildNodeFingerprintUnboundEvent names the binding by its identifiers only.
// The fingerprint hashes, identity key, and seen IPs never enter the event.
func buildNodeFingerprintUnboundEvent(operatorID, nodeID, fingerprintID, reason string, unbound quartermasterdb.UnboundNodeFingerprint) (*ipcpb.ServiceEvent, error) {
	before, err := structpb.NewStruct(map[string]any{
		"node_id":        nodeID,
		"fingerprint_id": fingerprintID,
	})
	if err != nil {
		return nil, err
	}
	return &ipcpb.ServiceEvent{
		EventType:    serviceevents.NodeFingerprintUnbound,
		Timestamp:    timestamppb.Now(),
		Source:       "quartermaster",
		UserId:       operatorID,
		ResourceType: "node_fingerprint",
		ResourceId:   fingerprintID,
		Payload: &ipcpb.ServiceEvent_ClusterEvent{ClusterEvent: &ipcpb.ClusterEvent{
			ClusterId:   unbound.ClusterID,
			TenantId:    unbound.TenantID,
			Reason:      reason,
			BeforeState: before,
		}},
	}, nil
}
