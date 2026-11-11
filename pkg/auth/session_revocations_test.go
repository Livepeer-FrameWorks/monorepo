package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestSessionRevocationKeepsOtherSessionsAndLegacyCutoff(t *testing.T) {
	cache := NewSessionRevocations()
	now := time.Now()
	cache.RecordSession("tenant", "user", "lost-device", now)
	for _, tc := range []struct {
		tenant, user, session string
		want                  bool
	}{
		{"tenant", "user", "lost-device", true},
		{"tenant", "user", "current", false},
		{"other-tenant", "user", "lost-device", false},
		{"tenant", "other-user", "lost-device", false},
		{"tenant", "user", "", false},
	} {
		if got := cache.SessionRevoked(tc.tenant, tc.user, tc.session, now.Add(time.Second)); got != tc.want {
			t.Fatalf("%+v: revoked=%v", tc, got)
		}
	}
	cache.Record("tenant", "user", now)
	if !cache.Revoked("tenant", "user", now) || cache.Revoked("tenant", "user", now.Add(time.Second)) {
		t.Fatal("legacy user-wide issue-time cutoff changed")
	}
}

func TestValidateRevocableJWTChecksStableSessionAfterRefresh(t *testing.T) {
	secret := []byte("test-session-secret")
	now := time.Now()
	cache := NewSessionRevocations()
	cache.RecordSession("tenant", "user", "lost-device", now.Add(-time.Minute))
	for _, sessionID := range []string{"lost-device", "current", ""} {
		claims := &Claims{TenantID: "tenant", UserID: "user", SessionID: sessionID,
			RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(SessionTokenTTL))},
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := ValidateRevocableSessionJWT(token, secret, cache)
		if sessionID == "lost-device" {
			if !errors.Is(err, ErrSessionRevoked) {
				t.Fatalf("refreshed revoked session accepted: %v", err)
			}
		} else if err != nil || parsed.SessionID != sessionID {
			t.Fatalf("unrelated/legacy session rejected: %v", err)
		}
	}
}
