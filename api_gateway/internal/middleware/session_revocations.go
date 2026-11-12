package middleware

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/sessionrevocations"
)

// ErrSessionRevoked rejects a session token whose user's sessions were
// revoked (sign-out, password reset, refresh-token reuse) at or after the
// second it was issued.
var ErrSessionRevoked = auth.ErrSessionRevoked

type SessionRevocations = auth.SessionRevocations

func NewSessionRevocations() *SessionRevocations { return auth.NewSessionRevocations() }

// sessionTokenCoveredBy reports whether a revocation at revokedAt covers a
// session token issued at issuedAt. Token issue times have whole-second
// precision, so a token issued in the revocation's second is covered.
func sessionTokenCoveredBy(issuedAt, revokedAt time.Time) bool {
	return issuedAt.Unix() <= revokedAt.Unix()
}

// enforcedSessionRevocations is the set every session-token check in this
// package consults; nil until Bridge enforces one.
var enforcedSessionRevocations atomic.Pointer[SessionRevocations]

// EnforceSessionRevocations makes every session-token check of this package
// (HTTP requests, the auth routes, GraphQL WebSockets) refuse tokens that
// revocations covers. Without it nothing is refused on that ground.
func EnforceSessionRevocations(revocations *SessionRevocations) {
	enforcedSessionRevocations.Store(revocations)
}

// validateSessionJWT verifies an interactive session token and refuses it
// with ErrSessionRevoked when this replica learned that its user's sessions
// were revoked after it was issued. A replica that has not learned of a
// revocation serves the locally valid token.
func validateSessionJWT(token string, secret []byte) (*auth.Claims, error) {
	return ValidateSessionJWT(token, secret)
}

// ValidateSessionJWT applies the same replica revocation state to handlers
// that need to recheck a session's signed platform privileges.
func ValidateSessionJWT(token string, secret []byte) (*auth.Claims, error) {
	return auth.ValidateRevocableSessionJWT(token, secret, enforcedSessionRevocations.Load())
}

// RecentSessionRevocations lists, across tenants, the session revocations
// committed after since: Commodore's ListRecentSessionRevocations.
type RecentSessionRevocations func(ctx context.Context, since time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error)

// StartSessionRevocations keeps the HTTP cache and WebSocket closures in the
// shared lifecycle. The existing confirmed Signalman subscription calls Resume.
func (s *WebsocketSessions) StartSessionRevocations(ctx context.Context, list RecentSessionRevocations, logger logging.Logger) *sessionrevocations.Feed {
	return sessionrevocations.Start(ctx, sessionrevocations.Config{
		Cache: s.revocations, List: sessionrevocations.Lister(list), Logger: logger,
		Applied: func(rev *commodorepb.SessionRevocation) {
			if rev.GetAccountDeactivated() {
				s.CloseDeactivatedUser(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
			} else if rev.GetSessionId() != "" {
				s.CloseSession(rev.GetTenantId(), rev.GetUserId(), rev.GetSessionId(), rev.GetRevokedAt().AsTime())
			} else {
				s.CloseUserSessions(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
			}
		},
	})
}
