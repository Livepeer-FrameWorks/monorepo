package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
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
func (s *CommodoreServer) RecordPullSourceEvent(ctx context.Context, req *pb.RecordPullSourceEventRequest) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetInternalName() == "" {
		return nil, status.Error(codes.InvalidArgument, "internal_name is required")
	}
	if req.GetEventKind() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_kind is required")
	}
	if _, execErr := s.db.ExecContext(ctx, `
		INSERT INTO commodore.pull_source_events
		            (tenant_id, stream_id, internal_name, event_kind, detail)
		VALUES      ($1::uuid, NULLIF($2, '')::uuid, $3, $4, NULLIF($5, ''))
	`, req.GetTenantId(), req.GetStreamId(), req.GetInternalName(), req.GetEventKind(), req.GetDetail()); execErr != nil {
		return nil, status.Errorf(codes.Internal, "pull_source_events insert failed: %v", execErr)
	}
	if req.GetEventKind() == "resolved" && req.GetStreamId() != "" {
		clusterID := strings.TrimSpace(req.GetDetail())
		if clusterID != "" {
			if _, execErr := s.db.ExecContext(ctx, `
				UPDATE commodore.streams
				   SET active_ingest_cluster_id = $1,
				       active_ingest_cluster_updated_at = NOW()
				 WHERE id = $2::uuid
				   AND tenant_id = $3::uuid
				   AND ingest_mode = 'pull'
			`, clusterID, req.GetStreamId(), req.GetTenantId()); execErr != nil {
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
func (s *CommodoreServer) ListPullSourceEvents(ctx context.Context, req *pb.ListPullSourceEventsRequest) (*pb.ListPullSourceEventsResponse, error) {
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

	var (
		rows *sql.Rows
		qErr error
	)
	if req.GetStreamId() != "" {
		rows, qErr = s.db.QueryContext(ctx, `
			SELECT id::text, COALESCE(stream_id::text, ''), internal_name, event_kind, COALESCE(detail, ''), created_at
			  FROM commodore.pull_source_events
			 WHERE tenant_id = $1::uuid AND stream_id = $2::uuid
			 ORDER BY created_at DESC
			 LIMIT $3
		`, tenantID, req.GetStreamId(), limit)
	} else {
		rows, qErr = s.db.QueryContext(ctx, `
			SELECT id::text, COALESCE(stream_id::text, ''), internal_name, event_kind, COALESCE(detail, ''), created_at
			  FROM commodore.pull_source_events
			 WHERE tenant_id = $1::uuid AND internal_name = $2
			 ORDER BY created_at DESC
			 LIMIT $3
		`, tenantID, req.GetInternalName(), limit)
	}
	if qErr != nil {
		if errors.Is(qErr, sql.ErrNoRows) {
			return &pb.ListPullSourceEventsResponse{}, nil
		}
		return nil, status.Errorf(codes.Internal, "pull_source_events query failed: %v", qErr)
	}
	defer func() { _ = rows.Close() }()

	out := &pb.ListPullSourceEventsResponse{}
	for rows.Next() {
		var (
			ev                                  pb.PullSourceEvent
			id, sid, internalName, kind, detail string
			createdAt                           = sql.NullTime{}
		)
		if scanErr := rows.Scan(&id, &sid, &internalName, &kind, &detail, &createdAt); scanErr != nil {
			return nil, status.Errorf(codes.Internal, "pull_source_events scan failed: %v", scanErr)
		}
		ev.Id = id
		ev.StreamId = sid
		ev.InternalName = internalName
		ev.EventKind = kind
		ev.Detail = detail
		if createdAt.Valid {
			ev.CreatedAt = timestamppb.New(createdAt.Time)
		}
		out.Events = append(out.Events, &ev)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, status.Errorf(codes.Internal, "pull_source_events iter failed: %v", rerr)
	}
	return out, nil
}
