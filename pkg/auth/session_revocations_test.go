package auth

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestNamedRevocationOutlivesTransactionTimestamp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		secret := []byte("revocation-retention")
		cache := NewSessionRevocations()
		token, err := GenerateNamedSessionJWT("user", "tenant", "user@example.test", "member", nil, "session", time.Now(), time.Now().Add(SessionTokenTTL), secret)
		if err != nil {
			t.Fatal(err)
		}
		// NOW() can precede issuance while the revoker waits for the issuer's lock.
		cache.RecordSession("tenant", "user", "session", time.Now().Add(-30*time.Second))
		time.Sleep(SessionTokenTTL + JWTClockSkewLeeway - 20*time.Second)
		cache.Prune()
		if _, err := ValidateInteractiveJWT(token, secret); err != nil {
			t.Fatalf("fixture JWT no longer verifies in clock leeway: %v", err)
		}
		if _, err := ValidateRevocableSessionJWT(token, secret, cache); !errors.Is(err, ErrSessionRevoked) {
			t.Fatalf("pruning forgot a known revocation while the token still verifies: %v", err)
		}
	})
}

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
