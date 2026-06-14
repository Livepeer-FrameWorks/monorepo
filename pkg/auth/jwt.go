package auth

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidJWT      = errors.New("invalid JWT token")
	ErrExpiredJWT      = errors.New("JWT token expired")
	ErrUnauthenticated = errors.New("authentication required")
)

// RolePlatformOperator is the authorization role granting access to
// platform-wide operator surfaces (/admin, Mist break-glass). It rides the
// RFC 9068 `roles` claim, not tenant membership.
const RolePlatformOperator = "platform_operator"

// SessionTokenTTL is the access-token lifetime. Short by design; grant/role
// changes propagate at the next refresh.
const SessionTokenTTL = 15 * time.Minute

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

// ValidateJWT validates a JWT token and returns its claims
func ValidateJWT(tokenString string, secret []byte) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		// Verify the signing method to prevent algorithm confusion attacks
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return secret, nil
	})

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
