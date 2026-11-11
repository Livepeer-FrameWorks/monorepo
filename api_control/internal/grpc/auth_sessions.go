package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func sessionOwner(ctx context.Context) (string, string, error) {
	if ctxkeys.GetAuthType(ctx) != "jwt" {
		return "", "", status.Error(codes.PermissionDenied, "interactive sign-in required")
	}
	return extractUserContext(ctx)
}

func sessionProto(row commodoredb.CommodoreAuthSession, currentID string) *commodorepb.AuthSession {
	expiry := row.IdleExpiresAt
	if row.AbsoluteExpiresAt.Before(expiry) {
		expiry = row.AbsoluteExpiresAt
	}
	return &commodorepb.AuthSession{
		Id: row.ID, ClientId: row.ClientID, AuthMethod: row.AuthMethod, DeviceLabel: row.DeviceLabel,
		ClientVersion: row.ClientVersion, LastIp: row.LastIp, UserAgent: row.UserAgent,
		CreatedAt: timestamppb.New(row.CreatedAt), LastSeenAt: timestamppb.New(row.LastSeenAt),
		ExpiresAt: timestamppb.New(expiry), Current: currentID != "" && row.ID == currentID,
	}
}

func (s *CommodoreServer) ListSessions(ctx context.Context, req *commodorepb.ListSessionsRequest) (*commodorepb.ListSessionsResponse, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	page, err := pagination.Parse(req.GetPagination())
	if err != nil || page.Direction != pagination.Forward {
		return nil, status.Error(codes.InvalidArgument, "sessions support first/after pagination")
	}
	if page.Limit > 100 {
		page.Limit = 100
	}
	params := commodoredb.ListAuthSessionsParams{TenantID: tenantID, UserID: userID, PageLimit: int32(page.Limit + 1)}
	if page.Cursor != nil {
		if _, cursorErr := uuid.Parse(page.Cursor.ID); cursorErr != nil || page.Cursor.IsSortKey {
			return nil, status.Error(codes.InvalidArgument, "invalid session cursor")
		}
		params.BeforeCreatedAt = sql.NullTime{Time: page.Cursor.Timestamp, Valid: true}
		params.BeforeID = sql.NullString{String: page.Cursor.ID, Valid: true}
	}
	queries := commodoredb.New(s.db)
	total, err := queries.CountAuthSessions(ctx, commodoredb.CountAuthSessionsParams{TenantID: tenantID, UserID: userID})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	rows, err := queries.ListAuthSessions(ctx, params)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	hasNext := len(rows) > page.Limit
	if hasNext {
		rows = rows[:page.Limit]
	}
	response := &commodorepb.ListSessionsResponse{
		Sessions:   make([]*commodorepb.AuthSession, 0, len(rows)),
		Pagination: &commonpb.CursorPaginationResponse{TotalCount: total, HasNextPage: hasNext, HasPreviousPage: page.Cursor != nil},
	}
	for _, row := range rows {
		response.Sessions = append(response.Sessions, sessionProto(row, ctxkeys.GetSessionID(ctx)))
	}
	if len(rows) != 0 {
		start := pagination.EncodeCursor(rows[0].CreatedAt, rows[0].ID)
		end := pagination.EncodeCursor(rows[len(rows)-1].CreatedAt, rows[len(rows)-1].ID)
		response.Pagination.StartCursor, response.Pagination.EndCursor = &start, &end
	}
	return response, nil
}

func (s *CommodoreServer) RenameSession(ctx context.Context, req *commodorepb.RenameSessionRequest) (*commodorepb.AuthSession, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	if _, idErr := uuid.Parse(req.GetSessionId()); idErr != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session ID")
	}
	label := strings.TrimSpace(req.GetLabel())
	if label == "" || !utf8.ValidString(label) || utf8.RuneCountInString(label) > 200 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return nil, status.Error(codes.InvalidArgument, "session label must contain 1 to 200 characters on one line")
	}
	queries := commodoredb.New(s.db)
	changed, err := queries.RenameAuthSession(ctx, commodoredb.RenameAuthSessionParams{
		TenantID: tenantID, UserID: userID, ID: req.GetSessionId(), DeviceLabel: label,
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	if changed == 0 {
		return nil, status.Error(codes.NotFound, "session not found")
	}
	row, err := queries.GetAuthSession(ctx, commodoredb.GetAuthSessionParams{TenantID: tenantID, UserID: userID, ID: req.GetSessionId()})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session state unavailable")
	}
	return sessionProto(row, ctxkeys.GetSessionID(ctx)), nil
}

func (s *CommodoreServer) finishAuthSessionRevocationTx(ctx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession, reason internalv1.SessionRevocationReason) error {
	queries := commodoredb.New(tx)
	// An older issuer locks its refresh row, then writes a successor without
	// sid. Wait for that rotation before taking the deletion statement's snapshot.
	if _, err := queries.LockAuthSessionRefreshTokens(ctx, commodoredb.LockAuthSessionRefreshTokensParams{
		TenantID: row.TenantID, UserID: row.UserID, SessionID: sql.NullString{String: row.ID, Valid: true},
	}); err != nil {
		return err
	}
	// Deleting the connected family makes old issuers return invalid_grant
	// instead of interpreting an explicit sign-out as user-wide token theft.
	if err := queries.RevokeAuthSessionRefreshTokens(ctx, commodoredb.RevokeAuthSessionRefreshTokensParams{
		TenantID: row.TenantID, UserID: row.UserID, SessionID: sql.NullString{String: row.ID, Valid: true},
	}); err != nil {
		return err
	}
	_, err := s.enqueueDomainEventTx(ctx, tx, row.TenantID, &domainEvent{
		msg:         &internalv1.AuthSessionRevoked{UserId: row.UserID, SessionId: row.ID, RevokedAt: timestamppb.New(row.RevokedAt.Time), Reason: reason},
		aggregateID: row.ID,
	}, row.RevokedAt.Time)
	return err
}

func (s *CommodoreServer) RevokeSession(ctx context.Context, req *commodorepb.RevokeSessionRequest) (*commodorepb.RevokeSessionResponse, error) {
	return s.revokeSession(ctx, req, "user")
}

func (s *CommodoreServer) revokeSession(ctx context.Context, req *commodorepb.RevokeSessionRequest, reason string) (*commodorepb.RevokeSessionResponse, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	if _, idErr := uuid.Parse(req.GetSessionId()); idErr != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session ID")
	}
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenantID, ID: userID}); lockErr != nil {
			return lockErr
		}
		row, queryErr := queries.LockAuthSession(ctx, commodoredb.LockAuthSessionParams{TenantID: tenantID, UserID: userID, ID: req.GetSessionId()})
		if errors.Is(queryErr, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "session not found")
		}
		if queryErr != nil || row.RevokedAt.Valid {
			return queryErr
		}
		row, queryErr = queries.RevokeAuthSession(ctx, commodoredb.RevokeAuthSessionParams{
			TenantID: tenantID, UserID: userID, ID: row.ID, RevokeReason: sql.NullString{String: reason, Valid: true},
		})
		if queryErr != nil {
			return queryErr
		}
		return s.finishAuthSessionRevocationTx(ctx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT)
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "session revocation unavailable")
	}
	return &commodorepb.RevokeSessionResponse{Success: true, Current: req.GetSessionId() == ctxkeys.GetSessionID(ctx)}, nil
}

func (s *CommodoreServer) RevokeOtherSessions(ctx context.Context, _ *emptypb.Empty) (*commodorepb.RevokeOtherSessionsResponse, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	currentID := ctxkeys.GetSessionID(ctx)
	if _, idErr := uuid.Parse(currentID); idErr != nil {
		return nil, status.Error(codes.FailedPrecondition, "sign in again before ending other sessions")
	}
	var count int32
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		count = 0
		queries := commodoredb.New(tx)
		if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenantID, ID: userID}); lockErr != nil {
			return lockErr
		}
		rows, revokeErr := queries.RevokeOtherAuthSessions(ctx, commodoredb.RevokeOtherAuthSessionsParams{
			TenantID: tenantID, UserID: userID, ID: currentID, RevokeReason: sql.NullString{String: "user", Valid: true},
		})
		if revokeErr != nil {
			return revokeErr
		}
		for _, row := range rows {
			if emitErr := s.finishAuthSessionRevocationTx(ctx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT); emitErr != nil {
				return emitErr
			}
		}
		count = int32(len(rows))
		return nil
	})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "session revocation unavailable")
	}
	return &commodorepb.RevokeOtherSessionsResponse{RevokedCount: count}, nil
}
