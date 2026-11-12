package resolvers

import (
	"context"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func securityActivityModel(row *periscopepb.SecurityActivityEvent) (*model.SecurityActivityEvent, error) {
	kinds := map[string]model.SecurityActivityKind{
		"auth_login_succeeded":  model.SecurityActivityKindSignInSucceeded,
		"auth_login_failed":     model.SecurityActivityKindSignInFailed,
		"auth_password_changed": model.SecurityActivityKindPasswordChanged,
		"auth_password_reset":   model.SecurityActivityKindPasswordReset,
		"auth_session.revoked":  model.SecurityActivityKindSessionRevoked,
		"user.sessions_revoked": model.SecurityActivityKindSessionsRevoked,
		"wallet_linked":         model.SecurityActivityKindWalletLinked,
		"wallet_unlinked":       model.SecurityActivityKindWalletUnlinked,
	}
	kind, ok := kinds[row.GetEventType()]
	if !ok {
		return nil, status.Error(codes.Unavailable, "security activity contains an unknown event")
	}
	if row.GetEventType() == "user.sessions_revoked" && row.GetReason() == "SESSION_REVOCATION_REASON_PASSWORD_RESET" {
		kind = model.SecurityActivityKindPasswordReset
	}
	event := &model.SecurityActivityEvent{ID: row.GetId(), Kind: kind, Timestamp: row.GetTimestamp().AsTime(), AuthMethod: authMethodModel(row.GetAuthMethod())}
	if row.GetSessionId() != "" {
		event.SessionID = strPtr(row.GetSessionId())
	}
	switch row.GetReason() {
	case "SESSION_REVOCATION_REASON_LOGOUT", "SESSION_REVOCATION_REASON_PASSWORD_RESET", "SESSION_REVOCATION_REASON_REFRESH_TOKEN_REUSE", "SESSION_REVOCATION_REASON_OPERATOR_DEACTIVATION", "SESSION_REVOCATION_REASON_PASSWORD_CHANGE":
		event.Reason = strPtr(row.GetReason())
	}
	return event, nil
}

func (r *Resolver) DoSecurityActivityConnection(ctx context.Context, first *int, after *string) (*model.SecurityActivityConnection, error) {
	if err := requireInteractiveSecurity(ctx); err != nil {
		return nil, err
	}
	limit := pagination.DefaultLimit
	if first != nil {
		if *first < 1 || *first > 100 {
			return nil, status.Error(codes.InvalidArgument, "first must be between 1 and 100")
		}
		limit = *first
	}
	response, err := r.Clients.Periscope.GetSecurityActivity(ctx, &commonpb.CursorPaginationRequest{First: int32(limit), After: after})
	if err != nil {
		return nil, err
	}
	page := response.GetPagination()
	connection := &model.SecurityActivityConnection{Nodes: []*model.SecurityActivityEvent{}, Edges: []*model.SecurityActivityEdge{}, TotalCount: int(page.GetTotalCount()), PageInfo: &model.PageInfo{HasNextPage: page.GetHasNextPage(), HasPreviousPage: page.GetHasPreviousPage()}}
	if page.GetStartCursor() != "" {
		connection.PageInfo.StartCursor = strPtr(page.GetStartCursor())
	}
	if page.GetEndCursor() != "" {
		connection.PageInfo.EndCursor = strPtr(page.GetEndCursor())
	}
	for _, row := range response.GetEvents() {
		event, conversionErr := securityActivityModel(row)
		if conversionErr != nil {
			return nil, conversionErr
		}
		connection.Nodes = append(connection.Nodes, event)
		connection.Edges = append(connection.Edges, &model.SecurityActivityEdge{Node: event, Cursor: pagination.EncodeCursor(row.GetTimestamp().AsTime(), row.GetId())})
	}
	return connection, nil
}
