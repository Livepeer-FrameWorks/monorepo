package grpc

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *CommodoreServer) ChangePassword(ctx context.Context, req *commodorepb.ChangePasswordRequest) (*commodorepb.ChangePasswordResponse, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	currentID := ctxkeys.GetSessionID(ctx)
	if _, err = uuid.Parse(currentID); err != nil {
		return nil, status.Error(codes.FailedPrecondition, "renew this session before changing your password")
	}
	if req.GetCurrentPassword() == "" || len(req.GetNewPassword()) < 8 || len(req.GetNewPassword()) > 72 {
		return nil, status.Error(codes.InvalidArgument, "current password and a new password of 8 to 72 bytes required")
	}
	queries := commodoredb.New(s.db)
	proofHash, err := queries.GetSessionPasswordHash(ctx, commodoredb.GetSessionPasswordHashParams{TenantID: tenantID, ID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "account unavailable")
	}
	if err != nil {
		return nil, sessionIssueError(err)
	}
	if !s.passwordMatches(req.GetCurrentPassword(), proofHash) {
		return nil, status.Error(codes.Unauthenticated, "current password is incorrect")
	}
	newHash, err := auth.HashPassword(req.GetNewPassword())
	if err != nil {
		return nil, status.Error(codes.Internal, "could not hash password")
	}
	var revokedCount int32
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		revokedCount = 0
		queries := commodoredb.New(tx)
		if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenantID, ID: userID}); lockErr != nil {
			return lockErr
		}
		current, lockErr := queries.LockAuthSession(ctx, commodoredb.LockAuthSessionParams{TenantID: tenantID, UserID: userID, ID: currentID})
		if errors.Is(lockErr, sql.ErrNoRows) || lockErr == nil && (current.RevokedAt.Valid || !sessionDeadline(current).After(time.Now())) {
			return status.Error(codes.Unauthenticated, "current session expired or revoked")
		}
		if lockErr != nil {
			return lockErr
		}
		currentHash, queryErr := queries.GetSessionPasswordHash(ctx, commodoredb.GetSessionPasswordHashParams{TenantID: tenantID, ID: userID})
		if queryErr != nil {
			return queryErr
		}
		if subtle.ConstantTimeCompare([]byte(proofHash), []byte(currentHash)) != 1 {
			return status.Error(codes.Unauthenticated, "credentials changed; enter your current password again")
		}
		if queryErr = queries.ChangeSessionPassword(ctx, commodoredb.ChangeSessionPasswordParams{TenantID: tenantID, ID: userID, PasswordHash: sql.NullString{String: newHash, Valid: true}}); queryErr != nil {
			return queryErr
		}
		rows, queryErr := queries.RevokeOtherAuthSessions(ctx, commodoredb.RevokeOtherAuthSessionsParams{
			TenantID: tenantID, UserID: userID, ID: currentID, RevokeReason: sql.NullString{String: "password_change", Valid: true},
		})
		if queryErr != nil {
			return queryErr
		}
		for _, row := range rows {
			if queryErr = s.finishAuthSessionRevocationTx(ctx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_PASSWORD_CHANGE); queryErr != nil {
				return queryErr
			}
		}
		// Old issuers can leave sid-less successors. Wait for them before the
		// deletion snapshot so they cannot refresh after the credential change.
		if queryErr = lockUserRefreshTokensTx(ctx, tx, userID, tenantID); queryErr != nil {
			return queryErr
		}
		if queryErr = queries.DeleteOtherLegacyRefreshTokens(ctx, commodoredb.DeleteOtherLegacyRefreshTokensParams{
			TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: currentID, Valid: true},
		}); queryErr != nil {
			return queryErr
		}
		if queryErr = s.enqueueAuthEventTx(ctx, tx, "auth_password_changed", userID, tenantID, "password", ""); queryErr != nil {
			return queryErr
		}
		revokedCount = int32(len(rows))
		return nil
	})
	if err != nil {
		return nil, sessionIssueError(err)
	}
	return &commodorepb.ChangePasswordResponse{Success: true, RevokedCount: revokedCount}, nil
}
