package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Bounds the stops one tenant reports. A tenant has one closure stop plus
	// one per deactivated publisher.
	pendingIngestStopsPerTenant = 100
	// Bounds the registry read and, separately, the concurrent Foghorn probes,
	// so retirement answers within the CLI's 15s operator deadline.
	retireIngestCellProbeWindow = 5 * time.Second
)

// Closure synchronization states reported to operators.
const (
	closureSyncOpen         = "open"
	closureSyncPending      = "pending"
	closureSyncRetrying     = "retrying"
	closureSyncSynchronized = "synchronized"
)

// AdminGetTenantAccountState reports closure synchronization and undelivered
// session stops. Only platform-operator JWTs may call it.
func (s *CommodoreServer) AdminGetTenantAccountState(ctx context.Context, req *commodorepb.AdminGetTenantAccountStateRequest) (*commodorepb.TenantAccountState, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(strings.TrimSpace(req.GetTenantId()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	return s.tenantAccountState(ctx, tenant.String())
}

func (s *CommodoreServer) tenantAccountState(ctx context.Context, tenantID string) (*commodorepb.TenantAccountState, error) {
	queries := commodoredb.New(s.db)
	out := &commodorepb.TenantAccountState{TenantId: tenantID, ClosureSync: closureSyncOpen}
	closure, err := queries.GetTenantAccountClosureState(ctx, tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, status.Errorf(codes.Internal, "read tenant closure state: %v", err)
	}
	if err == nil && closure.ClosedAt.Valid {
		out.ClosedAt = timestamppb.New(closure.ClosedAt.Time)
		out.ClosedBy = closure.ClosedBy
		out.ClosureError = closure.ClosureError
		out.ClosureFailures = closure.ClosureFailures
		out.QuartermasterClosedAt = optionalTimestamp(closure.QuartermasterClosedAt)
		out.ClosureAttemptedAt = optionalTimestamp(closure.ClosureAttemptedAt)
		out.ClosureRetryAfter = optionalTimestamp(closure.ClosureRetryAfter)
		out.ClosureSync = closureSyncState(closure)
	}
	stops, err := queries.ListPendingIngestStops(ctx, tenantID, pendingIngestStopsPerTenant+1)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read pending session stops: %v", err)
	}
	if len(stops) > pendingIngestStopsPerTenant {
		stops, out.PendingIngestStopsTruncated = stops[:pendingIngestStopsPerTenant], true
	}
	for _, stop := range stops {
		target := commodoredb.MediaAuthorityTarget{Key: stop.TargetKey, Kind: stop.TargetKind}
		targetID := target.ObjectID()
		if stop.TargetKind == commodoredb.MediaAuthorityTargetTenant {
			targetID = stop.TenantID
		}
		out.PendingIngestStops = append(out.PendingIngestStops, &commodorepb.PendingIngestStop{
			TargetKind: stop.TargetKind, TargetId: targetID, Status: stop.Status, Attempts: stop.Attempts,
			PendingSince: timestamppb.New(stop.PendingSince), NextAttemptAt: timestamppb.New(stop.NextAttemptAt),
			LastError: stop.LastError, Cells: stop.Cells, RetiredCells: stop.RetiredCells,
		})
	}
	return out, nil
}

// closureSyncState names a closed tenant's Quartermaster synchronization.
func closureSyncState(closure commodoredb.TenantAccountClosureState) string {
	switch {
	case !closure.ClosedAt.Valid:
		return closureSyncOpen
	case closure.QuartermasterClosedAt.Valid:
		return closureSyncSynchronized
	case closure.ClosureError != "":
		return closureSyncRetrying
	default:
		return closureSyncPending
	}
}

func optionalTimestamp(value sql.NullTime) *timestamppb.Timestamp {
	if !value.Valid {
		return nil
	}
	return timestamppb.New(value.Time)
}

// AdminRetireIngestCell records a permanently removed cell and retries the
// pending session stops that include it. A cell that answers is refused: its
// stops are delivered normally. Only platform-operator JWTs may call it.
func (s *CommodoreServer) AdminRetireIngestCell(ctx context.Context, req *commodorepb.AdminRetireIngestCellRequest) (*commodorepb.AdminRetireIngestCellResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	cell := strings.TrimSpace(req.GetClusterId())
	if cell == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id is required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if err := s.refuseAnsweringIngestCell(ctx, cell); err != nil {
		return nil, err
	}
	queries := commodoredb.New(s.db)
	record, created, err := queries.RetireIngestCell(ctx, cell, middleware.GetUserID(ctx), reason)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "record cell retirement: %v", err)
	}
	affected, _, err := queries.RetryIngestStopsForCell(ctx, cell)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "cell retired and the record kept; schedule its session stops: %v", err)
	}
	s.logger.WithFields(logging.Fields{"cell": cell, "retired_by": record.RetiredBy, "created": created, "pending_ingest_stops": affected}).Warn("Ingest cell retired by operator")
	return &commodorepb.AdminRetireIngestCellResponse{
		ClusterId: record.CellID, RetiredAt: timestamppb.New(record.RetiredAt), RetiredBy: record.RetiredBy,
		Reason: record.Reason, AlreadyRetired: !created, PendingIngestStops: int32(affected),
	}, nil
}

// AdminUnretireIngestCell removes a cell's retirement record, so a stop the
// cell does not confirm stays pending again. Removing a missing record
// succeeds with was_retired=false. Only platform-operator JWTs may call it.
func (s *CommodoreServer) AdminUnretireIngestCell(ctx context.Context, req *commodorepb.AdminUnretireIngestCellRequest) (*commodorepb.AdminUnretireIngestCellResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	cell := strings.TrimSpace(req.GetClusterId())
	if cell == "" {
		return nil, status.Error(codes.InvalidArgument, "cluster_id is required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	record, deleted, err := commodoredb.New(s.db).UnretireIngestCell(ctx, cell)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "remove cell retirement: %v", err)
	}
	out := &commodorepb.AdminUnretireIngestCellResponse{ClusterId: cell, WasRetired: deleted}
	if deleted {
		out.RetiredAt, out.RetiredBy, out.RetirementReason = timestamppb.New(record.RetiredAt), record.RetiredBy, record.Reason
		s.logger.WithFields(logging.Fields{
			"cell": cell, "unretired_by": middleware.GetUserID(ctx), "reason": reason,
			"retired_by": record.RetiredBy, "retired_at": record.RetiredAt, "retirement_reason": record.Reason,
		}).Warn("Ingest cell retirement removed by operator")
	}
	return out, nil
}

// refuseAnsweringIngestCell lets retirement proceed only when Quartermaster
// confirms the cell's registrations and none of its Foghorn instances, in any
// status, answers. An unreadable registry refuses: it cannot show the cell gone.
func (s *CommodoreServer) refuseAnsweringIngestCell(ctx context.Context, cell string) error {
	if s.quartermasterClient == nil || s.foghornPool == nil {
		return status.Error(codes.Unavailable, "Quartermaster and Foghorn connections are required to confirm the cell is gone")
	}
	discoverCtx, cancel := context.WithTimeout(ctx, retireIngestCellProbeWindow)
	defer cancel()
	registered, err := s.quartermasterClient.DiscoverServices(discoverCtx, "foghorn", cell, &commonpb.CursorPaginationRequest{First: 100})
	if err != nil {
		return status.Errorf(codes.Unavailable, "Quartermaster could not list the cell's Foghorn instances: %v", err)
	}
	var addrs []string
	for _, instance := range registered.GetInstances() {
		if addr := serviceInstanceAddr(instance); addr != "" {
			addrs = append(addrs, addr)
		}
	}
	addrs = dedupeAddrs(addrs...)
	answered := make([]bool, len(addrs))
	probeCtx, cancelProbe := context.WithTimeout(ctx, retireIngestCellProbeWindow)
	defer cancelProbe()
	var wg sync.WaitGroup
	for i, addr := range addrs {
		client, poolErr := s.foghornPool.GetOrCreate(foghornPoolKey(cell, addr), addr)
		if poolErr != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, probeErr := client.GetMediaCellPlacementCapability(probeCtx)
			answered[i] = probeErr == nil || !ingestCellUnreachable(probeErr)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	for i, addr := range addrs {
		if answered[i] {
			return status.Errorf(codes.FailedPrecondition, "cell %s answers at %s; its session stops are delivered normally, and only a permanently removed cell can be retired", cell, addr)
		}
	}
	return nil
}
