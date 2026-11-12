package resolvers

import (
	"context"
	"fmt"
	"net"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireInteractiveSecurity(ctx context.Context) error {
	if middleware.IsDemoMode(ctx) {
		return errDemoUnavailable("Account security")
	}
	if ctxkeys.GetUserID(ctx) == "" || ctxkeys.GetTenantID(ctx) == "" {
		return status.Error(codes.Unauthenticated, "Sign in to manage your sessions")
	}
	if ctxkeys.GetAuthType(ctx) != "jwt" {
		return status.Error(codes.PermissionDenied, "Interactive sign-in required")
	}
	return nil
}

func authSessionResultError[T any](err error) (T, error) {
	if mapped, ok := mapCommodoreErr(err).(T); ok {
		return mapped, nil
	}
	var zero T
	return zero, fmt.Errorf("account security request failed: %w", err)
}

func maskedAuthSessionIP(value string) *string {
	ip := net.ParseIP(value)
	if ip == nil {
		return nil
	}
	var masked string
	if v4 := ip.To4(); v4 != nil {
		masked = fmt.Sprintf("%d.%d.%d.x", v4[0], v4[1], v4[2])
	} else {
		masked = ip.Mask(net.CIDRMask(48, 128)).String() + "/48"
	}
	return &masked
}

func (r *Resolver) authSessionModel(row *commodorepb.AuthSession) *model.AuthSession {
	client := model.AuthClientKindUnknown
	switch row.GetClientId() {
	case "web":
		client = model.AuthClientKindBrowser
	case "cli":
		client = model.AuthClientKindCli
	case "tray-mac":
		client = model.AuthClientKindDesktop
	}
	method := model.AuthMethodUnknown
	switch row.GetAuthMethod() {
	case "password":
		method = model.AuthMethodPassword
	case "wallet":
		method = model.AuthMethodWallet
	case "pkce":
		method = model.AuthMethodBrowserHandoff
	case "device_code":
		method = model.AuthMethodDeviceCode
	}
	session := &model.AuthSession{ID: row.GetId(), ClientKind: client, ClientID: row.GetClientId(), AuthMethod: method, Label: row.GetDeviceLabel(), IPAddress: maskedAuthSessionIP(row.GetLastIp()), CreatedAt: row.GetCreatedAt().AsTime(), LastActiveAt: row.GetLastSeenAt().AsTime(), ExpiresAt: row.GetExpiresAt().AsTime(), Current: row.GetCurrent()}
	if version := row.GetClientVersion(); version != "" {
		session.ClientVersion = &version
	}
	if r.SessionGeoIP != nil {
		if location := r.SessionGeoIP.Lookup(row.GetLastIp()); location != nil {
			parts := []string{}
			if location.City != "" {
				parts = append(parts, location.City)
			}
			if location.CountryName != "" {
				parts = append(parts, location.CountryName)
			} else if location.CountryCode != "" {
				parts = append(parts, location.CountryCode)
			}
			if len(parts) > 0 {
				label := strings.Join(parts, ", ")
				session.Location = &label
			}
		}
	}
	return session
}

func (r *Resolver) DoAuthSessionsConnection(ctx context.Context, first *int, after *string) (*model.AuthSessionConnection, error) {
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
	response, err := r.Clients.Commodore.ListSessions(ctx, &commonpb.CursorPaginationRequest{First: int32(limit), After: after})
	if err != nil {
		return nil, err
	}
	page := response.GetPagination()
	connection := &model.AuthSessionConnection{Nodes: []*model.AuthSession{}, Edges: []*model.AuthSessionEdge{}, TotalCount: int(page.GetTotalCount()), PageInfo: &model.PageInfo{HasNextPage: page.GetHasNextPage(), HasPreviousPage: page.GetHasPreviousPage(), StartCursor: nil, EndCursor: nil}}
	if page.GetStartCursor() != "" {
		connection.PageInfo.StartCursor = strPtr(page.GetStartCursor())
	}
	if page.GetEndCursor() != "" {
		connection.PageInfo.EndCursor = strPtr(page.GetEndCursor())
	}
	for _, row := range response.GetSessions() {
		node := r.authSessionModel(row)
		connection.Nodes = append(connection.Nodes, node)
		connection.Edges = append(connection.Edges, &model.AuthSessionEdge{Node: node, Cursor: pagination.EncodeCursor(row.GetCreatedAt().AsTime(), row.GetId())})
	}
	return connection, nil
}

func (r *Resolver) DoRevokeAuthSession(ctx context.Context, id string) (model.RevokeAuthSessionResult, error) {
	if err := requireInteractiveSecurity(ctx); err != nil {
		return authSessionResultError[model.RevokeAuthSessionResult](err)
	}
	response, err := r.Clients.Commodore.RevokeSession(ctx, id)
	if err != nil {
		return authSessionResultError[model.RevokeAuthSessionResult](err)
	}
	return &model.RevokeAuthSessionPayload{Success: response.GetSuccess(), Current: response.GetCurrent()}, nil
}

func (r *Resolver) DoRevokeOtherAuthSessions(ctx context.Context) (model.RevokeOtherAuthSessionsResult, error) {
	if err := requireInteractiveSecurity(ctx); err != nil {
		return authSessionResultError[model.RevokeOtherAuthSessionsResult](err)
	}
	if ctxkeys.GetSessionID(ctx) == "" {
		return securitySessionRefreshRequired(), nil
	}
	response, err := r.Clients.Commodore.RevokeOtherSessions(ctx)
	if err != nil {
		return authSessionResultError[model.RevokeOtherAuthSessionsResult](err)
	}
	return &model.RevokeOtherAuthSessionsPayload{RevokedCount: int(response.GetRevokedCount())}, nil
}

func (r *Resolver) DoRenameAuthSession(ctx context.Context, id, label string) (model.RenameAuthSessionResult, error) {
	if err := requireInteractiveSecurity(ctx); err != nil {
		return authSessionResultError[model.RenameAuthSessionResult](err)
	}
	response, err := r.Clients.Commodore.RenameSession(ctx, id, label)
	if err != nil {
		return authSessionResultError[model.RenameAuthSessionResult](err)
	}
	return r.authSessionModel(response), nil
}

func (r *Resolver) DoChangePassword(ctx context.Context, currentPassword, newPassword string) (model.ChangePasswordResult, error) {
	if err := requireInteractiveSecurity(ctx); err != nil {
		return authSessionResultError[model.ChangePasswordResult](err)
	}
	if ctxkeys.GetSessionID(ctx) == "" {
		return securitySessionRefreshRequired(), nil
	}
	response, err := r.Clients.Commodore.ChangePassword(ctx, currentPassword, newPassword)
	if err != nil {
		return authSessionResultError[model.ChangePasswordResult](err)
	}
	return &model.ChangePasswordPayload{Success: response.GetSuccess(), RevokedCount: int(response.GetRevokedCount())}, nil
}

func securitySessionRefreshRequired() *model.ValidationError {
	return &model.ValidationError{Message: "Renew this session before changing account security", Code: strPtr("SESSION_REFRESH_REQUIRED")}
}
