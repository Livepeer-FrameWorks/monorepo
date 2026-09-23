package grpc

import (
	"context"
	"sort"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// acceptedAPITokenPermissions is allowedAPITokenPermissions as a sorted slice,
// the form the operator listing binds as its accepted-set parameter.
func acceptedAPITokenPermissions() []string {
	out := make([]string, 0, len(allowedAPITokenPermissions))
	for permission := range allowedAPITokenPermissions {
		out = append(out, permission)
	}
	sort.Strings(out)
	return out
}

func unsupportedAPITokenPermissions(permissions []string) []string {
	var out []string
	for _, permission := range permissions {
		if _, ok := allowedAPITokenPermissions[permission]; !ok {
			out = append(out, permission)
		}
	}
	return out
}

// AdminListAPITokens lists token metadata for platform operators. It is an
// explicit cross-tenant read: an operator auditing which tokens a release no
// longer accepts has to see every tenant, so tenant_id narrows the listing
// instead of scoping it to the caller. Only an operator JWT reaches the query.
func (s *CommodoreServer) AdminListAPITokens(ctx context.Context, req *commodorepb.AdminListAPITokensRequest) (*commodorepb.AdminListAPITokensResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	if tenantID != "" {
		if _, err := uuid.Parse(tenantID); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
		}
	}
	params, err := pagination.Parse(req.GetPagination())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pagination: %v", err)
	}

	queries := commodoredb.New(s.db)
	filter := commodoredb.AdminAPITokenFilter{
		TenantID:        tenantID,
		UnsupportedOnly: req.GetUnsupportedScopesOnly(),
		Accepted:        acceptedAPITokenPermissions(),
	}
	total, err := queries.CountAdminAPITokens(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	page := commodoredb.AdminAPITokenPage{
		Backward: params.Direction == pagination.Backward,
		RowLimit: int32(params.Limit + 1),
	}
	if params.Cursor != nil {
		cursorTime := params.Cursor.Timestamp
		page.CursorTime = &cursorTime
		page.CursorID = params.Cursor.ID
	}
	rows, err := queries.ListAdminAPITokens(ctx, filter, page)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "database error: %v", err)
	}
	fetched := len(rows)
	if fetched > params.Limit {
		rows = rows[:params.Limit]
	}
	if page.Backward {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}

	tokens := make([]*commodorepb.AdminAPITokenInfo, 0, len(rows))
	for _, row := range rows {
		if !row.CreatedAt.Valid {
			return nil, status.Errorf(codes.Internal, "API token %s has NULL created_at", row.ID)
		}
		permissions := []string(row.Permissions)
		token := &commodorepb.AdminAPITokenInfo{
			Id:                     row.ID,
			TenantId:               row.TenantID,
			TokenName:              row.TokenName,
			Permissions:            permissions,
			UnsupportedPermissions: unsupportedAPITokenPermissions(permissions),
			Status:                 row.Status,
			CreatedAt:              timestamppb.New(row.CreatedAt.Time),
		}
		if row.LastUsedAt.Valid {
			token.LastUsedAt = timestamppb.New(row.LastUsedAt.Time)
		}
		if row.ExpiresAt.Valid {
			token.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
		}
		tokens = append(tokens, token)
	}

	var startCursor, endCursor string
	if len(tokens) > 0 {
		startCursor = pagination.EncodeCursor(tokens[0].CreatedAt.AsTime(), tokens[0].Id)
		endCursor = pagination.EncodeCursor(tokens[len(tokens)-1].CreatedAt.AsTime(), tokens[len(tokens)-1].Id)
	}
	return &commodorepb.AdminListAPITokensResponse{
		Tokens:     tokens,
		Pagination: pagination.BuildResponse(fetched, params.Limit, params.Direction, int32(total), startCursor, endCursor),
	}, nil
}
