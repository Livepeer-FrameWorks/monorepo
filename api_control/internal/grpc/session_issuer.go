package grpc

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net"
	"strings"
	"time"
	"unicode"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxLiveAuthSessions = 100

type SessionContext struct {
	AuthMethod, ClientID, DeviceLabel, ClientVersion, IP, UserAgent string
	passwordHash                                                    string
}

func boundedSessionText(value string, limit int) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, "")))
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit])
	}
	return value
}

func sessionContextFromRequest(ctx context.Context, method, client string) SessionContext {
	md, _ := metadata.FromIncomingContext(ctx)
	first := func(key string) string {
		values := md.Get(key)
		if len(values) == 0 {
			return ""
		}
		return values[0]
	}
	ip := ""
	if parsed := net.ParseIP(first("x-client-ip")); parsed != nil {
		ip = parsed.String()
	}
	label := boundedSessionText(first("x-device-label-bin"), 200)
	if label == "" {
		switch client {
		case "cli":
			label = "FrameWorks CLI"
		case "tray-mac":
			label = "FrameWorks for Mac"
		default:
			label = browserSessionLabel(first("x-client-user-agent-bin"))
		}
	}
	return SessionContext{AuthMethod: method, ClientID: client, DeviceLabel: label,
		ClientVersion: boundedSessionText(first("x-client-version-bin"), 100), IP: ip,
		UserAgent: boundedSessionText(first("x-client-user-agent-bin"), 1024)}
}

func sessionIssueError(err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	return status.Error(codes.Unavailable, "session state unavailable")
}

func sessionDeadline(row commodoredb.CommodoreAuthSession) time.Time {
	if row.AbsoluteExpiresAt.Before(row.IdleExpiresAt) {
		return row.AbsoluteExpiresAt
	}
	return row.IdleExpiresAt
}

func (s *CommodoreServer) sessionResponse(row commodoredb.CommodoreAuthSession, user commodoreUserRecord, refresh string) (*commodorepb.AuthResponse, error) {
	expires := time.Now().Add(auth.SessionTokenTTL).Truncate(time.Second)
	if deadline := sessionDeadline(row); deadline.Before(expires) {
		expires = deadline.Truncate(time.Second)
	}
	if !expires.After(time.Now()) {
		return nil, status.Error(codes.Unauthenticated, "session expired")
	}
	token, err := auth.GenerateNamedSessionJWT(row.UserID, row.TenantID, user.Email, user.Role,
		platformRoles(user.PlatformOperator), row.ID, row.CreatedAt, expires, s.settings().JWTSecret)
	if err != nil {
		return nil, err
	}
	return &commodorepb.AuthResponse{Token: token, RefreshToken: refresh,
		User: user.toProtoUser(row.UserID, row.TenantID), ExpiresAt: timestamppb.New(expires)}, nil
}

// All interactive issuance holds the owner lock through cap enforcement and commit.
func (s *CommodoreServer) issueUserSessionTx(ctx context.Context, tx *sql.Tx, userID, tenantID string, info SessionContext) (*commodorepb.AuthResponse, error) {
	if info.ClientID != "web" && info.ClientID != "cli" && info.ClientID != "tray-mac" {
		return nil, status.Error(codes.InvalidArgument, "unknown sign-in client")
	}
	queries := commodoredb.New(tx)
	if _, err := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenantID, ID: userID}); err != nil {
		return nil, err
	}
	userRow, err := queries.GetRefreshUser(ctx, commodoredb.GetRefreshUserParams{ID: userID, TenantID: tenantID})
	if err != nil {
		return nil, err
	}
	user, err := commodoreUserFromRefreshRow(userRow)
	if err != nil {
		return nil, err
	}
	if !user.IsActive {
		return nil, status.Error(codes.Unauthenticated, "account deactivated")
	}
	if info.AuthMethod == "password" {
		currentHash, queryErr := queries.GetSessionPasswordHash(ctx, commodoredb.GetSessionPasswordHashParams{TenantID: tenantID, ID: userID})
		if queryErr != nil {
			return nil, queryErr
		}
		// Password comparison happens outside this lock; a reset must invalidate
		// that proof before a newly authenticated session can be created.
		if subtle.ConstantTimeCompare([]byte(info.passwordHash), []byte(currentHash)) != 1 || !user.IsVerified {
			return nil, status.Error(codes.Unauthenticated, "credentials changed; sign in again")
		}
	}
	count, err := queries.CountAuthSessions(ctx, commodoredb.CountAuthSessionsParams{TenantID: tenantID, UserID: userID})
	if err != nil {
		return nil, err
	}
	if count >= maxLiveAuthSessions {
		rows, queryErr := queries.ListOldestAuthSessions(ctx, commodoredb.ListOldestAuthSessionsParams{TenantID: tenantID, UserID: userID, Limit: count - maxLiveAuthSessions + 1})
		if queryErr != nil {
			return nil, queryErr
		}
		for _, row := range rows {
			revoked, revokeErr := queries.RevokeAuthSession(ctx, commodoredb.RevokeAuthSessionParams{
				TenantID: tenantID, UserID: userID, ID: row.ID, RevokeReason: sql.NullString{String: "expired", Valid: true}})
			if revokeErr != nil {
				return nil, revokeErr
			}
			if revokeErr = s.finishAuthSessionRevocationTx(ctx, tx, revoked, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT); revokeErr != nil {
				return nil, revokeErr
			}
		}
	}
	now := time.Now()
	lifetime := 90 * 24 * time.Hour
	if info.ClientID == "cli" || info.ClientID == "tray-mac" {
		lifetime = 180 * 24 * time.Hour
	}
	row, err := queries.InsertAuthSession(ctx, commodoredb.InsertAuthSessionParams{
		TenantID: tenantID, UserID: userID, AuthMethod: info.AuthMethod, ClientID: info.ClientID,
		DeviceLabel: boundedSessionText(info.DeviceLabel, 200), ClientVersion: boundedSessionText(info.ClientVersion, 100),
		CreatedIp: info.IP, UserAgent: boundedSessionText(info.UserAgent, 1024),
		DeviceKey:     sessionDeviceKey(info.ClientID, boundedSessionText(info.DeviceLabel, 200)),
		IdleExpiresAt: now.Add(30 * 24 * time.Hour), AbsoluteExpiresAt: now.Add(lifetime),
	})
	if err != nil {
		return nil, err
	}
	if err = s.recordSessionDeviceTx(ctx, tx, row, user.Email); err != nil {
		return nil, err
	}
	refresh, err := generateRandomString(40)
	if err != nil {
		return nil, err
	}
	if _, err = queries.InsertSessionRefreshToken(ctx, commodoredb.InsertSessionRefreshTokenParams{
		TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: row.ID, Valid: true},
		TokenHash: hashToken(refresh), ExpiresAt: sessionDeadline(row),
	}); err != nil {
		return nil, err
	}
	if err = s.enqueueAuthEventTx(ctx, tx, eventAuthLoginSucceeded, userID, tenantID, info.AuthMethod, ""); err != nil {
		return nil, err
	}
	return s.sessionResponse(row, user, refresh)
}

// A concurrent loser gets a retryable response; it never creates a second head.
// An unused successor after the grace period can be replaced to recover a lost response.
func (s *CommodoreServer) RefreshToken(ctx context.Context, req *commodorepb.RefreshTokenRequest) (*commodorepb.AuthResponse, error) {
	if req.GetRefreshToken() == "" {
		return nil, status.Error(codes.InvalidArgument, "refresh token required")
	}
	hash := hashToken(req.GetRefreshToken())
	owner, err := commodoredb.New(s.db).GetRefreshTokenOwnerByHash(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired refresh token")
	}
	if err != nil {
		return nil, sessionIssueError(err)
	}
	var response *commodorepb.AuthResponse
	var rejected bool
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		response, rejected = nil, false
		queries := commodoredb.New(tx)
		if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: owner.TenantID, ID: owner.UserID}); lockErr != nil {
			if errors.Is(lockErr, sql.ErrNoRows) {
				return status.Error(codes.Unauthenticated, "refresh owner no longer exists")
			}
			return lockErr
		}
		token, queryErr := queries.LockRefreshTokenByHash(ctx, hash)
		if errors.Is(queryErr, sql.ErrNoRows) {
			return status.Error(codes.Unauthenticated, "invalid or expired refresh token")
		}
		if queryErr != nil {
			return queryErr
		}
		if token.UserID != owner.UserID || token.TenantID != owner.TenantID {
			return status.Error(codes.Unauthenticated, "invalid refresh owner")
		}
		sid, queryErr := queries.GetRefreshTokenSession(ctx, commodoredb.GetRefreshTokenSessionParams{TenantID: owner.TenantID, UserID: owner.UserID, ID: token.ID})
		if queryErr != nil {
			return queryErr
		}
		if !sid.Valid {
			sid, queryErr = queries.AdoptAuthSessionRefreshFamily(ctx, commodoredb.AdoptAuthSessionRefreshFamilyParams{TenantID: owner.TenantID, UserID: owner.UserID, ID: token.ID})
			if errors.Is(queryErr, sql.ErrNoRows) || queryErr == nil && !sid.Valid {
				return status.Error(codes.Unauthenticated, "invalid refresh family")
			}
			if queryErr != nil {
				return queryErr
			}
		}
		row, queryErr := queries.LockAuthSession(ctx, commodoredb.LockAuthSessionParams{TenantID: owner.TenantID, UserID: owner.UserID, ID: sid.String})
		if errors.Is(queryErr, sql.ErrNoRows) {
			return status.Error(codes.Unauthenticated, "invalid refresh session")
		}
		if queryErr != nil {
			return queryErr
		}
		if row.RevokedAt.Valid || !sessionDeadline(row).After(time.Now()) {
			return status.Error(codes.Unauthenticated, "session expired or revoked")
		}
		staleID := ""
		if token.Revoked {
			if token.RotatedAt.Valid && time.Since(token.RotatedAt.Time) <= refreshTokenReuseGracePeriod {
				return status.Error(codes.Unavailable, "refresh already rotated; retry with the current credential")
			}
			if token.RotatedAt.Valid && token.ReplacedBy.Valid {
				successor, successorErr := queries.GetSessionRefreshSuccessor(ctx, commodoredb.GetSessionRefreshSuccessorParams{TenantID: owner.TenantID, UserID: owner.UserID, ID: token.ReplacedBy.String})
				if successorErr != nil && !errors.Is(successorErr, sql.ErrNoRows) {
					return successorErr
				}
				if successorErr == nil && !successor.Revoked && !successor.ReplacedBy.Valid {
					staleID = successor.ID
				}
			}
			if staleID == "" {
				revoked, revokeErr := queries.RevokeAuthSession(ctx, commodoredb.RevokeAuthSessionParams{
					TenantID: owner.TenantID, UserID: owner.UserID, ID: row.ID, RevokeReason: sql.NullString{String: "reuse_detected", Valid: true}})
				if revokeErr != nil {
					return revokeErr
				}
				if revokeErr = s.finishAuthSessionRevocationTx(ctx, tx, revoked, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_REFRESH_TOKEN_REUSE); revokeErr != nil {
					return revokeErr
				}
				rejected = true
				return nil
			}
		}
		userRow, queryErr := queries.GetRefreshUser(ctx, commodoredb.GetRefreshUserParams{ID: owner.UserID, TenantID: owner.TenantID})
		if queryErr != nil {
			return queryErr
		}
		user, conversionErr := commodoreUserFromRefreshRow(userRow)
		if conversionErr != nil {
			return conversionErr
		}
		if !user.IsActive {
			return status.Error(codes.Unauthenticated, "account deactivated")
		}
		row, queryErr = queries.TouchAuthSession(ctx, commodoredb.TouchAuthSessionParams{TenantID: owner.TenantID, UserID: owner.UserID, ID: row.ID, LastIp: sessionContextFromRequest(ctx, "", "").IP})
		if errors.Is(queryErr, sql.ErrNoRows) {
			return status.Error(codes.Unauthenticated, "session expired or revoked")
		}
		if queryErr != nil {
			return queryErr
		}
		if row.DeviceKey != "" {
			if queryErr = queries.RememberSessionDevice(ctx, commodoredb.RememberSessionDeviceParams{TenantID: row.TenantID, UserID: row.UserID, DeviceKey: row.DeviceKey}); queryErr != nil {
				return queryErr
			}
		}
		refresh, issueErr := generateRandomString(40)
		if issueErr != nil {
			return issueErr
		}
		newID, issueErr := queries.InsertSessionRefreshToken(ctx, commodoredb.InsertSessionRefreshTokenParams{
			TenantID: owner.TenantID, UserID: owner.UserID, SessionID: sid, TokenHash: hashToken(refresh), ExpiresAt: sessionDeadline(row)})
		if issueErr != nil {
			return issueErr
		}
		if staleID != "" {
			if issueErr = queries.RevokeRefreshTokenByID(ctx, staleID); issueErr != nil {
				return issueErr
			}
		}
		// Resetting rotated_at also serializes a second lost-response recovery.
		if issueErr = queries.RotateRefreshToken(ctx, commodoredb.RotateRefreshTokenParams{ID: token.ID, ReplacedBy: sql.NullString{String: newID, Valid: true}}); issueErr != nil {
			return issueErr
		}
		if issueErr = s.enqueueAuthEventTx(ctx, tx, eventAuthTokenRefreshed, owner.UserID, owner.TenantID, "refresh_token", ""); issueErr != nil {
			return issueErr
		}
		response, issueErr = s.sessionResponse(row, user, refresh)
		return issueErr
	})
	if err != nil {
		if status.Code(err) == codes.Unknown {
			s.logger.WithError(err).Error("Failed to refresh session")
		}
		return nil, sessionIssueError(err)
	}
	if rejected {
		return nil, status.Error(codes.Unauthenticated, "session invalidated")
	}
	return response, nil
}
