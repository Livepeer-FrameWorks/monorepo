package testutil

import (
	"time"

	"frameworks/pkg/auth"

	"github.com/golang-jwt/jwt/v5"
)

// JWTTestHelper provides utilities for JWT testing
type JWTTestHelper struct {
	Secret []byte
}

// NewJWTTestHelper creates a new JWT test helper with a default test secret
func NewJWTTestHelper() *JWTTestHelper {
	return &JWTTestHelper{
		Secret: []byte("test-secret-for-unit-tests"),
	}
}

// NewJWTTestHelperWithSecret creates a new JWT test helper with a custom secret
func NewJWTTestHelperWithSecret(secret []byte) *JWTTestHelper {
	return &JWTTestHelper{
		Secret: secret,
	}
}

// GenerateValidJWT generates a valid JWT token for testing
func (h *JWTTestHelper) GenerateValidJWT(userID, tenantID, email, role string) (string, error) {
	return auth.GenerateJWT(userID, tenantID, email, role, h.Secret)
}

// GenerateExpiredJWT generates an expired JWT token for testing
func (h *JWTTestHelper) GenerateExpiredJWT(userID, tenantID, email, role string) (string, error) {
	claims := &auth.Claims{
		UserID:   userID,
		TenantID: tenantID,
		Email:    email,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // Expired 1 hour ago
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)), // Issued 2 hours ago
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.Secret)
}

// GenerateJWTWithCustomExpiry generates a JWT with custom expiry time
func (h *JWTTestHelper) GenerateJWTWithCustomExpiry(userID, tenantID, email, role string, expiresAt time.Time) (string, error) {
	claims := &auth.Claims{
		UserID:   userID,
		TenantID: tenantID,
		Email:    email,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(h.Secret)
}

// GenerateMalformedJWT generates a malformed JWT for testing error scenarios
func (h *JWTTestHelper) GenerateMalformedJWT() string {
	return "invalid.jwt.token.format"
}

// GenerateJWTWithWrongSecret generates a JWT with wrong secret for testing
func (h *JWTTestHelper) GenerateJWTWithWrongSecret(userID, tenantID, email, role string) (string, error) {
	wrongSecret := []byte("wrong-secret")
	return auth.GenerateJWT(userID, tenantID, email, role, wrongSecret)
}

// GenerateJWTWithNoneAlgorithm generates a JWT with "none" algorithm (security vulnerability test)
func (h *JWTTestHelper) GenerateJWTWithNoneAlgorithm(userID, tenantID, email, role string) (string, error) {
	claims := &auth.Claims{
		UserID:   userID,
		TenantID: tenantID,
		Email:    email,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	return token.SignedString(jwt.UnsafeAllowNoneSignatureType)
}

// ValidateJWT validates a JWT using the test helper's secret
func (h *JWTTestHelper) ValidateJWT(tokenString string) (*auth.Claims, error) {
	return auth.ValidateJWT(tokenString, h.Secret)
}

// TestUser represents a test user for JWT generation
type TestUser struct {
	UserID   string
	TenantID string
	Email    string
	Role     string
}

// DefaultTestUser returns a default test user
func DefaultTestUser() TestUser {
	return TestUser{
		UserID:   "test-user-123",
		TenantID: "test-tenant-456",
		Email:    "test@example.com",
		Role:     "user",
	}
}

// AdminTestUser returns an admin test user
func AdminTestUser() TestUser {
	return TestUser{
		UserID:   "admin-user-999",
		TenantID: "test-tenant-456",
		Email:    "admin@example.com",
		Role:     "admin",
	}
}

// GenerateJWT generates a JWT for the test user
func (u TestUser) GenerateJWT(helper *JWTTestHelper) (string, error) {
	return helper.GenerateValidJWT(u.UserID, u.TenantID, u.Email, u.Role)
}

// GenerateExpiredJWT generates an expired JWT for the test user
func (u TestUser) GenerateExpiredJWT(helper *JWTTestHelper) (string, error) {
	return helper.GenerateExpiredJWT(u.UserID, u.TenantID, u.Email, u.Role)
}

// TestUsers for multi-tenant testing
var (
	TestUserTenant1 = TestUser{
		UserID:   "user-tenant1",
		TenantID: "tenant-1",
		Email:    "user1@example.com",
		Role:     "user",
	}

	TestUserTenant2 = TestUser{
		UserID:   "user-tenant2",
		TenantID: "tenant-2",
		Email:    "user2@example.com",
		Role:     "user",
	}

	TestAdminTenant1 = TestUser{
		UserID:   "admin-tenant1",
		TenantID: "tenant-1",
		Email:    "admin1@example.com",
		Role:     "admin",
	}
)
