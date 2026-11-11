package middleware

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// ErrSessionRevoked rejects a session token whose user's sessions were
// revoked (sign-out, password reset, refresh-token reuse) at or after the
// second it was issued.
var ErrSessionRevoked = auth.ErrSessionRevoked

// sessionRevocationRetention is how long a revocation can still concern a
// verifying token: every token it covers was issued no later than the
// revocation and expires a session lifetime (plus clock leeway) after that.
const sessionRevocationRetention = auth.SessionTokenTTL + auth.JWTClockSkewLeeway

const (
	sessionBackfillTimeout    = 10 * time.Second
	sessionBackfillRetryFirst = time.Second
	sessionBackfillRetryMax   = 30 * time.Second
)

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

// ResumeSessionRevocations is called each time this replica's platform-wide
// stream of session revocations becomes subscribed (first and after every
// reconnect): it reads the revocations a stream that was not subscribed may
// have missed and applies each like one that arrived on the stream. A failed
// read is retried with backoff until one succeeds; meanwhile tokens are
// judged on what the replica already knows. Calls during a read are merged
// into one more read after it.
func (s *WebsocketSessions) ResumeSessionRevocations(list RecentSessionRevocations, logger logging.Logger) {
	if s == nil || list == nil {
		return
	}
	s.mu.Lock()
	s.backfillPending = true
	if s.backfilling {
		s.mu.Unlock()
		return
	}
	s.backfilling = true
	s.mu.Unlock()
	go s.backfillSessionRevocations(list, logger)
}

func (s *WebsocketSessions) backfillSessionRevocations(list RecentSessionRevocations, logger logging.Logger) {
	retry := sessionBackfillRetryFirst
	for {
		s.mu.Lock()
		if !s.backfillPending {
			s.backfilling = false
			s.mu.Unlock()
			return
		}
		s.backfillPending = false
		s.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), sessionBackfillTimeout)
		resp, err := list(ctx, time.Now().Add(-sessionRevocationRetention))
		cancel()
		if err != nil {
			if logger != nil {
				logger.WithError(err).WithField("retry_in", retry.String()).Warn("Failed to read recent session revocations")
			}
			time.Sleep(retry)
			retry = min(retry*2, sessionBackfillRetryMax)
			s.mu.Lock()
			s.backfillPending = true
			s.mu.Unlock()
			continue
		}
		retry = sessionBackfillRetryFirst
		for _, rev := range resp.GetRevocations() {
			if !rev.GetRevokedAt().IsValid() {
				continue
			}
			if rev.GetAccountDeactivated() {
				s.CloseDeactivatedUser(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
			} else if rev.GetSessionId() != "" {
				s.CloseSession(rev.GetTenantId(), rev.GetUserId(), rev.GetSessionId(), rev.GetRevokedAt().AsTime())
			} else {
				s.CloseUserSessions(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
			}
		}
	}
}
