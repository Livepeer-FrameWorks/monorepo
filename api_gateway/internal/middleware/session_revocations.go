package middleware

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// ErrSessionRevoked rejects a session token whose user's sessions were
// revoked (sign-out, password reset, refresh-token reuse) at or after the
// second it was issued.
var ErrSessionRevoked = errors.New("session signed out")

// sessionRevocationRetention is how long a revocation can still concern a
// verifying token: every token it covers was issued no later than the
// revocation and expires a session lifetime (plus clock leeway) after that.
const sessionRevocationRetention = auth.SessionTokenTTL + auth.JWTClockSkewLeeway

const (
	sessionBackfillTimeout    = 10 * time.Second
	sessionBackfillRetryFirst = time.Second
	sessionBackfillRetryMax   = 30 * time.Second
)

// SessionRevocations holds, per user, the latest revocation of the user's
// sessions this replica learned, for as long as a token it covers can still
// verify. A session token issued in or before that second is refused.
type SessionRevocations struct {
	mu      sync.RWMutex
	cutoffs map[websocketUserKey]time.Time
}

// NewSessionRevocations returns an empty set.
func NewSessionRevocations() *SessionRevocations {
	return &SessionRevocations{cutoffs: map[websocketUserKey]time.Time{}}
}

// Record keeps revokedAt as the user's cutoff unless a later one is held,
// and forgets cutoffs that no verifying token can still predate.
func (r *SessionRevocations) Record(tenantID, userID string, revokedAt time.Time) {
	if r == nil || tenantID == "" || userID == "" {
		return
	}
	key := websocketUserKey{tenantID: tenantID, userID: userID}
	expired := time.Now().Add(-sessionRevocationRetention)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, cutoff := range r.cutoffs {
		if cutoff.Before(expired) {
			delete(r.cutoffs, k)
		}
	}
	if prev, ok := r.cutoffs[key]; !ok || revokedAt.After(prev) {
		r.cutoffs[key] = revokedAt
	}
}

// Revoked reports whether a session token of the user issued at issuedAt is
// covered by a recorded revocation.
func (r *SessionRevocations) Revoked(tenantID, userID string, issuedAt time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	cutoff, ok := r.cutoffs[websocketUserKey{tenantID: tenantID, userID: userID}]
	r.mu.RUnlock()
	return ok && sessionTokenCoveredBy(issuedAt, cutoff)
}

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
	claims, err := auth.ValidateInteractiveJWT(token, secret)
	if err != nil {
		return nil, err
	}
	if claims.IssuedAt != nil && enforcedSessionRevocations.Load().Revoked(claims.TenantID, claims.UserID, claims.IssuedAt.Time) {
		return nil, ErrSessionRevoked
	}
	return claims, nil
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
			if rev.GetRevokedAt().IsValid() {
				s.CloseUserSessions(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
			}
		}
	}
}
