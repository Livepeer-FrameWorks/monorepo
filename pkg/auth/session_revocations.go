package auth

import (
	"errors"
	"sync"
	"time"
)

var ErrSessionRevoked = errors.New("session signed out")

type sessionRevocationKey struct{ tenant, user, session string }

// SessionRevocations retains revocations while an affected access JWT can verify.
// An empty session identifies the legacy user-wide issue-time cutoff.
type SessionRevocations struct {
	mu      sync.RWMutex
	cutoffs map[sessionRevocationKey]time.Time
}

func NewSessionRevocations() *SessionRevocations {
	return &SessionRevocations{cutoffs: make(map[sessionRevocationKey]time.Time)}
}

func (r *SessionRevocations) Record(tenantID, userID string, revokedAt time.Time) {
	r.record(tenantID, userID, "", revokedAt)
}

func (r *SessionRevocations) RecordSession(tenantID, userID, sessionID string, revokedAt time.Time) {
	if sessionID == "" {
		return
	}
	r.record(tenantID, userID, sessionID, revokedAt)
}

func (r *SessionRevocations) record(tenantID, userID, sessionID string, revokedAt time.Time) {
	if r == nil || tenantID == "" || userID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	expired := time.Now().Add(-SessionTokenTTL - JWTClockSkewLeeway)
	for key, at := range r.cutoffs {
		if at.Before(expired) {
			delete(r.cutoffs, key)
		}
	}
	if r.cutoffs == nil {
		r.cutoffs = make(map[sessionRevocationKey]time.Time)
	}
	key := sessionRevocationKey{tenantID, userID, sessionID}
	if previous, ok := r.cutoffs[key]; !ok || revokedAt.After(previous) {
		r.cutoffs[key] = revokedAt
	}
}

func (r *SessionRevocations) Revoked(tenantID, userID string, issuedAt time.Time) bool {
	return r.SessionRevoked(tenantID, userID, "", issuedAt)
}

func (r *SessionRevocations) SessionRevoked(tenantID, userID, sessionID string, issuedAt time.Time) bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	cutoff, userRevoked := r.cutoffs[sessionRevocationKey{tenantID, userID, ""}]
	if userRevoked && issuedAt.Unix() <= cutoff.Unix() {
		return true
	}
	if sessionID != "" {
		_, revoked := r.cutoffs[sessionRevocationKey{tenantID, userID, sessionID}]
		return revoked
	}
	return false
}

// ValidateRevocableSessionJWT preserves user-wide revocation for legacy tokens
// and rejects a revoked sid regardless of the refreshed token's issue time.
func ValidateRevocableSessionJWT(token string, secret []byte, revocations *SessionRevocations) (*Claims, error) {
	claims, err := ValidateInteractiveJWT(token, secret)
	if err != nil {
		return nil, err
	}
	var issuedAt time.Time
	if claims.IssuedAt != nil {
		issuedAt = claims.IssuedAt.Time
	}
	if revocations.SessionRevoked(claims.TenantID, claims.UserID, claims.SessionID, issuedAt) {
		return nil, ErrSessionRevoked
	}
	return claims, nil
}
