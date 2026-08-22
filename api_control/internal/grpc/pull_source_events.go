package grpc

import (
	"context"
	"database/sql"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxPullSourceEventLimit     = 200
	defaultPullSourceEventLimit = 50
)

// RecordPullSourceEvent appends a row to commodore.pull_source_events. Writes
// are best-effort from Foghorn's perspective — the resolution path stays
// non-blocking on failure (the trigger handler doesn't wait on this).
//
// tenant_id is required so the row is correctly attributed; stream_id can be
// empty when resolution couldn't reach a tenant (e.g. commodore_error).
func (s *CommodoreServer) RecordPullSourceEvent(ctx context.Context, req *commodorepb.RecordPullSourceEventRequest) (*emptypb.Empty, error) {
	// SERVICE-TOKEN ONLY, checked before the insert and before the placement
	// write below. The tenant, stream, and cluster all come from the request,
	// and the shared interceptor also accepts JWTs — so without this a
	// logged-in user could write another tenant's source-event history and
	// steer a pull stream's placement.
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "RecordPullSourceEvent requires service token auth")
	}
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if req.GetEventKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_kind is required")
	}
	queries := commodoredb.New(s.db)
	if execErr := queries.InsertPullSourceEvent(ctx, commodoredb.InsertPullSourceEventParams{
		TenantID:     req.GetTenantId(),
		StreamID:     req.GetStreamId(),
		InternalName: req.GetInternalName(),
		EventKind:    req.GetEventKind(),
		Detail:       req.GetDetail(),
	}); execErr != nil {
		return nil, status.Errorf(codes.Internal, "pull_source_events insert failed: %v", execErr)
	}
	if req.GetEventKind() == "resolved" && req.GetStreamId() != "" {
		clusterID := strings.TrimSpace(req.GetDetail())
		if clusterID != "" {
			// Owned and contention-guarded like every other writer of this
			// column. A pull stream's placement is owned by the stream (its
			// resolver is the single writer for it), so an unrelated cluster's
			// stale resolve cannot take over a live placement, and a push
			// publisher's token-owned claim is never overwritten. Restricted to
			// ingest_mode='pull' as before, which already excludes push rows.
			if execErr := queries.StampResolvedPullStreamPlacement(ctx, commodoredb.StampResolvedPullStreamPlacementParams{
				ClusterID:    sql.NullString{String: clusterID, Valid: true},
				ClaimID:      sql.NullString{String: pullClaimToken(req.GetStreamId()), Valid: true},
				StreamID:     req.GetStreamId(),
				TenantID:     req.GetTenantId(),
				LeaseSeconds: int64(activeIngestLease.Seconds()),
			}); execErr != nil {
				s.logger.WithError(execErr).WithField("stream_id", req.GetStreamId()).Warn("Failed to stamp pull stream active ingest cluster")
			}
		}
	}
	return &emptypb.Empty{}, nil
}

// ListPullSourceEvents returns the most recent N events for a stream. Either
// stream_id or internal_name must be supplied; both filter the same way.
// Tenant scoping is enforced from the caller's JWT — this is the operator-
// facing read for the webapp's pull source health view.
func (s *CommodoreServer) ListPullSourceEvents(ctx context.Context, req *commodorepb.ListPullSourceEventsRequest) (*commodorepb.ListPullSourceEventsResponse, error) {
	_, tenantID, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetStreamId() == "" && req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "stream_id or internal_name is required")
	}
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultPullSourceEventLimit
	}
	if limit > maxPullSourceEventLimit {
		limit = maxPullSourceEventLimit
	}

	queries := commodoredb.New(s.db)
	out := &commodorepb.ListPullSourceEventsResponse{}
	appendEvent := func(id, streamID, internalName, kind, detail string, createdAt sql.NullTime) {
		event := &commodorepb.PullSourceEvent{
			Id: id, StreamId: streamID, InternalName: internalName, EventKind: kind, Detail: detail,
		}
		if createdAt.Valid {
			event.CreatedAt = timestamppb.New(createdAt.Time)
		}
		out.Events = append(out.Events, event)
	}
	if req.GetStreamId() != "" {
		rows, queryErr := queries.ListPullSourceEventsByStream(ctx, commodoredb.ListPullSourceEventsByStreamParams{
			TenantID: tenantID, StreamID: req.GetStreamId(), RowLimit: int32(limit),
		})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "pull_source_events query failed: %v", queryErr)
		}
		for _, row := range rows {
			appendEvent(row.ID, row.StreamID, row.InternalName, row.EventKind, row.Detail, row.CreatedAt)
		}
	} else {
		rows, queryErr := queries.ListPullSourceEventsByInternalName(ctx, commodoredb.ListPullSourceEventsByInternalNameParams{
			TenantID: tenantID, InternalName: req.GetInternalName(), RowLimit: int32(limit),
		})
		if queryErr != nil {
			return nil, status.Errorf(codes.Internal, "pull_source_events query failed: %v", queryErr)
		}
		for _, row := range rows {
			appendEvent(row.ID, row.StreamID, row.InternalName, row.EventKind, row.Detail, row.CreatedAt)
		}
	}
	return out, nil
}
