package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var testSecret = []byte("test-secret-please-do-not-use-in-prod")

func TestMistAdminSessionRoundTrip(t *testing.T) {
	tok, exp, err := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "media-us-1", 0, testSecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	if time.Until(exp) < 4*time.Minute || time.Until(exp) > 6*time.Minute {
		t.Errorf("default TTL not ~5min: %v", time.Until(exp))
	}

	claims, err := ValidateMistAdminSessionJWT(tok, testSecret, "edge-us-1")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.UserID != "u1" || claims.TenantID != "t1" || claims.Role != "owner" {
		t.Errorf("claims mismatch: %+v", claims)
	}
	if claims.NodeID != "edge-us-1" || claims.ClusterID != "media-us-1" {
		t.Errorf("node binding wrong: %+v", claims)
	}
	if claims.Purpose != MistAdminSessionPurpose {
		t.Errorf("purpose: %q", claims.Purpose)
	}
	if len(claims.Audience) != 1 || claims.Audience[0] != MistAdminSessionAudience {
		t.Errorf("audience: %v", claims.Audience)
	}
	if _, err := ValidateInteractiveJWT(tok, testSecret); !errors.Is(err, ErrDelegatedJWT) {
		t.Fatalf("Mist credential authenticated as an interactive session: %v", err)
	}
}

func TestMistAdminSessionRejectsWrongNode(t *testing.T) {
	tok, _, err := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "c1", 0, testSecret)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = ValidateMistAdminSessionJWT(tok, testSecret, "edge-eu-1")
	if !errors.Is(err, ErrWrongMistAdminSessionNode) {
		t.Errorf("expected ErrWrongMistAdminSessionNode; got %v", err)
	}
}

func TestMistAdminSessionRejectsEmptyExpectedNode(t *testing.T) {
	tok, _, _ := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "c1", 0, testSecret)
	_, err := ValidateMistAdminSessionJWT(tok, testSecret, "")
	if !errors.Is(err, ErrWrongMistAdminSessionNode) {
		t.Errorf("empty expectedNodeID must not match any token; got %v", err)
	}
}

func TestMistAdminSessionRejectsExpired(t *testing.T) {
	now := time.Now()
	claims := &MistAdminSessionClaims{
		Purpose: MistAdminSessionPurpose,
		NodeID:  "edge-us-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{MistAdminSessionAudience},
			IssuedAt:  jwt.NewNumericDate(now.Add(-2 * JWTClockSkewLeeway)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-JWTClockSkewLeeway - time.Second)),
		},
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = ValidateMistAdminSessionJWT(tok, testSecret, "edge-us-1")
	if !errors.Is(err, ErrExpiredMistAdminSession) {
		t.Errorf("expected ErrExpiredMistAdminSession; got %v", err)
	}
}

func TestMistAdminSessionRejectsWrongSecret(t *testing.T) {
	tok, _, _ := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "c1", 0, testSecret)
	_, err := ValidateMistAdminSessionJWT(tok, []byte("other-secret"), "edge-us-1")
	if !errors.Is(err, ErrInvalidMistAdminSession) {
		t.Errorf("expected ErrInvalidMistAdminSession; got %v", err)
	}
}

func TestMistAdminSessionRejectsWrongAudience(t *testing.T) {
	now := time.Now()
	claims := &MistAdminSessionClaims{
		Purpose: MistAdminSessionPurpose,
		NodeID:  "edge-us-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{"gateway"},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = ValidateMistAdminSessionJWT(signed, testSecret, "edge-us-1")
	if !errors.Is(err, ErrWrongMistAdminSessionAudience) {
		t.Fatalf("wrong audience error = %v, want ErrWrongMistAdminSessionAudience", err)
	}
}

func TestMistAdminSessionRequiresExpiration(t *testing.T) {
	now := time.Now()
	claims := &MistAdminSessionClaims{
		Purpose: MistAdminSessionPurpose,
		NodeID:  "edge-us-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{MistAdminSessionAudience},
			IssuedAt: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err = ValidateMistAdminSessionJWT(signed, testSecret, "edge-us-1"); !errors.Is(err, ErrInvalidMistAdminSession) {
		t.Fatalf("missing expiry error = %v, want ErrInvalidMistAdminSession", err)
	}
}

func TestMistAdminSessionAllowsClockSkew(t *testing.T) {
	now := time.Now()
	claims := &MistAdminSessionClaims{
		Purpose: MistAdminSessionPurpose,
		NodeID:  "edge-us-1",
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{MistAdminSessionAudience},
			IssuedAt:  jwt.NewNumericDate(now.Add(JWTClockSkewLeeway - time.Second)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(testSecret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err = ValidateMistAdminSessionJWT(signed, testSecret, "edge-us-1"); err != nil {
		t.Fatalf("within-leeway token rejected: %v", err)
	}
}

func TestMistAdminSessionRejectsLoginJWT(t *testing.T) {
	// A plain login JWT (different claims, no purpose) must NOT validate
	// as a mist-admin session. The purpose claim is the discriminator.
	loginTok, err := GenerateJWT("u1", "t1", "u@example.com", "owner", testSecret)
	if err != nil {
		t.Fatalf("login mint: %v", err)
	}
	_, err = ValidateMistAdminSessionJWT(loginTok, testSecret, "edge-us-1")
	if err == nil {
		t.Error("plain login JWT must not be accepted as mist-admin session")
	}
}

func TestMistAdminSessionRequiresNodeID(t *testing.T) {
	_, _, err := GenerateMistAdminSessionJWT("u1", "t1", "owner", "", "c1", 0, testSecret)
	if err == nil {
		t.Error("expected error when node_id is empty")
	}
}

func TestMistAdminSessionUniqueJTI(t *testing.T) {
	a, _, _ := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "c1", 0, testSecret)
	b, _, _ := GenerateMistAdminSessionJWT("u1", "t1", "owner", "edge-us-1", "c1", 0, testSecret)
	if a == b {
		t.Error("two sequentially-minted tokens must differ (jti uniqueness)")
	}
}

func TestCanAdminMistNode(t *testing.T) {
	cases := []struct {
		name          string
		ownerTenantID string
		callerTenant  string
		callerRole    string
		platformOp    bool
		want          bool
	}{
		{"owner-tenant-owner", "tenant-acme", "tenant-acme", "owner", false, true},
		{"owner-tenant-admin", "tenant-acme", "tenant-acme", "admin", false, true},
		{"owner-tenant-member-denied", "tenant-acme", "tenant-acme", "member", false, false},
		{"customer-owner-denied-on-other-node", "tenant-acme", "tenant-customer", "owner", false, false},
		{"platform-operator-break-glass-no-owner", "", "tenant-x", "member", true, true},
		{"platform-operator-break-glass-other-tenant", "tenant-acme", "tenant-x", "member", true, true},
		{"non-operator-different-tenant-denied", "tenant-acme", "tenant-evil", "owner", false, false},
		{"missing-owner-non-operator-denied", "", "tenant-acme", "owner", false, false},
		{"missing-caller-tenant-denied", "tenant-acme", "", "owner", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CanAdminMistNode(context.Background(), tc.ownerTenantID, tc.callerTenant, tc.callerRole, tc.platformOp)
			if got != tc.want {
				t.Errorf("CanAdminMistNode(owner=%q, caller=%q, role=%q, op=%v) = %v; want %v",
					tc.ownerTenantID, tc.callerTenant, tc.callerRole, tc.platformOp, got, tc.want)
			}
		})
	}
}
