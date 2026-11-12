package grpc

import (
	"context"
	"time"

	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *PeriscopeServer) GetSecurityActivity(ctx context.Context, req *periscopepb.GetSecurityActivityRequest) (*periscopepb.GetSecurityActivityResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "jwt" {
		return nil, status.Error(codes.PermissionDenied, "interactive sign-in required")
	}
	tenant, user := ctxkeys.GetTenantID(ctx), ctxkeys.GetUserID(ctx)
	tenantUUID, tenantErr := uuid.Parse(tenant)
	if tenantErr != nil {
		return nil, status.Error(codes.Unauthenticated, "signed-in tenant required")
	}
	userUUID, userErr := uuid.Parse(user)
	if userErr != nil {
		return nil, status.Error(codes.Unauthenticated, "signed-in user required")
	}
	tenant, user = tenantUUID.String(), userUUID.String()
	page := req.GetPagination()
	limit := int(page.GetFirst())
	if limit == 0 {
		limit = pagination.DefaultLimit
	}
	if limit < 1 || limit > 100 || page.GetLast() != 0 || page.GetBefore() != "" {
		return nil, status.Error(codes.InvalidArgument, "use forward pagination with first between 1 and 100")
	}
	now := time.Now().UTC()
	from := now.Add(-90 * 24 * time.Hour)
	before, beforeID := now.Add(time.Second), "ffffffff-ffff-ffff-ffff-ffffffffffff"
	after := page.GetAfter()
	if after != "" {
		cursor, err := pagination.DecodeCursor(after)
		if err != nil || cursor == nil || cursor.IsSortKey || cursor.Timestamp.After(now) {
			return nil, status.Error(codes.InvalidArgument, "invalid security activity cursor")
		}
		id, idErr := uuid.Parse(cursor.ID)
		if idErr != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid security activity cursor")
		}
		before, beforeID = cursor.Timestamp, id.String()
	}
	queryCtx, cancel := withClickhouseTimeout(ctx)
	defer cancel()
	var count int32
	if err := periscopequerydb.QueryRow(queryCtx, s.clickhouse, periscopequerydb.SecurityActivityCountSQL, tenant, user, from, now).Scan(&count); err != nil {
		s.logger.WithError(err).Warn("Security activity count unavailable")
		return nil, status.Error(codes.Unavailable, "security activity unavailable")
	}
	rows, err := periscopequerydb.Query(queryCtx, s.clickhouse, periscopequerydb.SecurityActivityPageSQL, tenant, user, from, now, before, beforeID, limit+1)
	if err != nil {
		s.logger.WithError(err).Warn("Security activity page unavailable")
		return nil, status.Error(codes.Unavailable, "security activity unavailable")
	}
	defer rows.Close()
	response := &periscopepb.GetSecurityActivityResponse{Events: []*periscopepb.SecurityActivityEvent{}, Pagination: &commonpb.CursorPaginationResponse{TotalCount: count, HasPreviousPage: after != ""}}
	for rows.Next() {
		event := &periscopepb.SecurityActivityEvent{}
		var at time.Time
		if err = rows.Scan(&event.Id, &event.EventType, &at, &event.AuthMethod, &event.SessionId, &event.Reason); err != nil {
			return nil, status.Error(codes.Unavailable, "security activity unavailable")
		}
		event.Timestamp = timestamppb.New(at.UTC())
		response.Events = append(response.Events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, status.Error(codes.Unavailable, "security activity unavailable")
	}
	if len(response.Events) > limit {
		response.Events = response.Events[:limit]
		response.Pagination.HasNextPage = true
	}
	if len(response.Events) > 0 {
		first, last := response.Events[0], response.Events[len(response.Events)-1]
		start, end := pagination.EncodeCursor(first.Timestamp.AsTime(), first.Id), pagination.EncodeCursor(last.Timestamp.AsTime(), last.Id)
		response.Pagination.StartCursor = &start
		response.Pagination.EndCursor = &end
	}
	return response, nil
}
