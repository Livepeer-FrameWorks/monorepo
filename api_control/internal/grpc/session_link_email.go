package grpc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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

// LinkEmail creates password credentials for an interactive wallet account and
// atomically ends its other sessions while preserving the current refresh family.
func (s *CommodoreServer) LinkEmail(ctx context.Context, req *commodorepb.LinkEmailRequest) (*commodorepb.LinkEmailResponse, error) {
	userID, tenantID, err := sessionOwner(ctx)
	if err != nil {
		return nil, err
	}
	currentID := ctxkeys.GetSessionID(ctx)
	if _, idErr := uuid.Parse(currentID); idErr != nil {
		return nil, status.Error(codes.FailedPrecondition, "renew this session before linking email")
	}
	email, password := normalizeEmail(req.GetEmail()), req.GetPassword()
	if email == "" || len(password) < 8 || len(password) > 72 {
		return nil, status.Error(codes.InvalidArgument, "email and password of 8 to 72 bytes required")
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		return nil, status.Error(codes.Internal, "could not hash password")
	}
	var tokenBytes [32]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return nil, status.Error(codes.Internal, "could not create verification token")
	}
	verificationHash := hashToken(hex.EncodeToString(tokenBytes[:]))
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		if _, txErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenantID, ID: userID}); txErr != nil {
			return txErr
		}
		current, txErr := queries.LockAuthSession(ctx, commodoredb.LockAuthSessionParams{TenantID: tenantID, UserID: userID, ID: currentID})
		if errors.Is(txErr, sql.ErrNoRows) || txErr == nil && (current.RevokedAt.Valid || !sessionDeadline(current).After(time.Now())) {
			return status.Error(codes.Unauthenticated, "current session expired or revoked")
		}
		if txErr != nil {
			return txErr
		}
		state, txErr := queries.GetUserSessionState(ctx, commodoredb.GetUserSessionStateParams{ID: userID, TenantID: tenantID})
		if txErr != nil {
			return txErr
		}
		if !state.IsActive {
			return status.Error(codes.Unauthenticated, "current session expired or revoked")
		}
		existing, txErr := queries.GetUserEmail(ctx, commodoredb.GetUserEmailParams{ID: userID, TenantID: tenantID})
		if txErr != nil {
			return txErr
		}
		if existing.Valid && existing.String != "" {
			return status.Error(codes.AlreadyExists, "email already linked to your account")
		}
		_, txErr = queries.FindOtherUserIDByEmail(ctx, commodoredb.FindOtherUserIDByEmailParams{Email: sql.NullString{String: email, Valid: true}, ID: userID})
		if txErr == nil {
			return status.Error(codes.AlreadyExists, "email already in use by another account")
		}
		if !errors.Is(txErr, sql.ErrNoRows) {
			return txErr
		}
		if txErr = queries.LinkUserEmail(ctx, commodoredb.LinkUserEmailParams{
			Email: sql.NullString{String: email, Valid: true}, PasswordHash: sql.NullString{String: passwordHash, Valid: true},
			VerificationToken: sql.NullString{String: verificationHash, Valid: true}, TokenExpiresAt: sql.NullTime{Time: time.Now().Add(24 * time.Hour), Valid: true},
			ID: userID, TenantID: tenantID,
		}); txErr != nil {
			return txErr
		}
		// Credential creation also invalidates any outstanding reset proof.
		if txErr = queries.ChangeSessionPassword(ctx, commodoredb.ChangeSessionPasswordParams{
			TenantID: tenantID, ID: userID, PasswordHash: sql.NullString{String: passwordHash, Valid: true},
		}); txErr != nil {
			return txErr
		}
		rows, txErr := queries.RevokeOtherAuthSessions(ctx, commodoredb.RevokeOtherAuthSessionsParams{
			TenantID: tenantID, UserID: userID, ID: currentID, RevokeReason: sql.NullString{String: "password_change", Valid: true},
		})
		if txErr != nil {
			return txErr
		}
		for _, row := range rows {
			if txErr = s.finishAuthSessionRevocationTx(ctx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_PASSWORD_CHANGE); txErr != nil {
				return txErr
			}
		}
		if txErr = lockUserRefreshTokensTx(ctx, tx, userID, tenantID); txErr != nil {
			return txErr
		}
		if txErr = queries.DeleteOtherLegacyRefreshTokens(ctx, commodoredb.DeleteOtherLegacyRefreshTokensParams{
			TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: currentID, Valid: true},
		}); txErr != nil {
			return txErr
		}
		if txErr = s.enqueueAuthEventTx(ctx, tx, "auth_password_changed", userID, tenantID, "wallet", ""); txErr != nil {
			return txErr
		}
		return enqueueAccountEmailTx(ctx, tx, userID, tenantID, accountEmailPurposeVerification)
	})
	if err != nil {
		var sqlState interface{ SQLState() string }
		if errors.As(err, &sqlState) && sqlState.SQLState() == "23505" {
			return nil, status.Error(codes.AlreadyExists, "email already in use by another account")
		}
		return nil, sessionIssueError(err)
	}
	s.kickAccountEmailOutbox()
	return &commodorepb.LinkEmailResponse{Success: true, Message: "Email linked. Verification email queued.", VerificationSent: true}, nil
}
