package auth

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var (
	ErrInvalidJWT      = errors.New("invalid JWT token")
	ErrExpiredJWT      = errors.New("JWT token expired")
	ErrUnauthenticated = errors.New("authentication required")
	ErrDelegatedJWT    = errors.New("delegated JWT is not an interactive session")
)

// RolePlatformOperator is the authorization role granting access to
// platform-wide operator surfaces (/admin, Mist break-glass). It rides the
// RFC 9068 `roles` claim, not tenant membership.
const RolePlatformOperator = "platform_operator"

// SessionTokenTTL is the access-token lifetime. Short by design; grant/role
// changes propagate at the next refresh.
const SessionTokenTTL = 15 * time.Minute

// DelegatedAPITokenTTL bounds how long an internal service may rely on a
// Gateway-validated API token before the original credential is checked again.
const DelegatedAPITokenTTL = time.Minute

// JWTClockSkewLeeway tolerates small host clock differences while preserving
// the deliberately short delegated-token lifetime.
const JWTClockSkewLeeway = 5 * time.Second

// Claims represents JWT claims with tenant context.
type Claims struct {
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	// Roles carries authorization attributes per RFC 9068 (the IANA-registered
	// "roles" claim); platform_operator is the only value today. Authorization
	// checks read Roles; the singular Role above stays the tenant role.
	Roles []string `json:"roles,omitempty"`
	// AuthTime is the Unix time of the authentication event (OIDC `auth_time`).
	// Set at mint; no authorization check reads it today.
	AuthTime int64 `json:"auth_time,omitempty"`
	// AuthType distinguishes short-lived API-token delegations from interactive
	// sessions. Empty means an interactive session for backwards compatibility.
	AuthType    string   `json:"auth_type,omitempty"`
	TokenID     string   `json:"token_id,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
	jwt.RegisteredClaims
}

// HasRole reports whether the token carries the given RFC 9068 role.
func (c *Claims) HasRole(role string) bool {
	return slices.Contains(c.Roles, role)
}

// GenerateSessionJWT mints a web session token carrying RFC 9068 `roles` and
// the `auth_time` of the authentication event. roles may be nil; a zero
// authTime defaults to now.
func GenerateSessionJWT(userID, tenantID, email, role string, roles []string, authTime time.Time, secret []byte) (string, error) {
	now := time.Now()
	if authTime.IsZero() {
		authTime = now
	}
	claims := &Claims{
		UserID:   userID,
		TenantID: tenantID,
		Email:    email,
		Role:     role,
		Roles:    roles,
		AuthTime: authTime.Unix(),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(SessionTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// GenerateJWT mints a web session token with no authorization roles, for
// callers that assert none.
func GenerateJWT(userID, tenantID, email, role string, secret []byte) (string, error) {
	return GenerateSessionJWT(userID, tenantID, email, role, nil, time.Time{}, secret)
}

// GenerateDelegatedAPITokenJWT mints the short-lived assertion Gateway sends
// to a named downstream service after Commodore validates an API token.
func GenerateDelegatedAPITokenJWT(userID, tenantID, email, role, tokenID string, permissions []string, audience string, secret []byte) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserID:      userID,
		TenantID:    tenantID,
		Email:       email,
		Role:        role,
		AuthType:    "api_token",
		TokenID:     tokenID,
		Permissions: slices.Clone(permissions),
		RegisteredClaims: jwt.RegisteredClaims{
			Audience:  jwt.ClaimStrings{audience},
			Subject:   tokenID,
			ID:        uuid.NewString(),
			ExpiresAt: jwt.NewNumericDate(now.Add(DelegatedAPITokenTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ValidateJWT validates a JWT token and returns its claims
func ValidateJWT(tokenString string, secret []byte) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		// Verify the signing method to prevent algorithm confusion attacks
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuedAt(), jwt.WithLeeway(JWTClockSkewLeeway))

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredJWT
		}
		return nil, ErrInvalidJWT
	}

	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}

	return nil, ErrInvalidJWT
}

// ValidateInteractiveJWT validates a browser/session JWT and rejects internal
// API-token assertions. Delegated assertions share the signing key so every
// non-gRPC ingress must make this distinction explicitly.
func ValidateInteractiveJWT(tokenString string, secret []byte) (*Claims, error) {
	claims, err := ValidateJWT(tokenString, secret)
	if err != nil {
		return nil, err
	}
	if claims.AuthType == "api_token" || len(claims.Audience) > 0 {
		return nil, ErrDelegatedJWT
	}
	return claims, nil
}
