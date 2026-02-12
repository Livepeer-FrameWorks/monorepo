package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashAndCheck(t *testing.T) {
	hash, err := HashPassword("secret", bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if !CheckPassword("secret", hash) {
		t.Fatalf("password should match")
	}
	if CheckPassword("wrong", hash) {
		t.Fatalf("password should not match")
	}
}

func TestValidateServiceToken(t *testing.T) {
	if err := ValidateServiceToken("", "expected"); err == nil {
		t.Fatalf("expected missing token error")
	}
	if err := ValidateServiceToken("bad", "expected"); err == nil {
		t.Fatalf("expected invalid token error")
	}
	if err := ValidateServiceToken("expected", "expected"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestJWTGenerateValidate(t *testing.T) {
	secret := []byte("s3cr3t")
	token, err := GenerateJWT("user1", "tenant1", "u@example.com", "admin", secret)
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}
	claims, err := ValidateJWT(token, secret)
	if err != nil {
		t.Fatalf("validate jwt: %v", err)
	}
	if claims.UserID != "user1" || claims.TenantID != "tenant1" {
		t.Fatalf("claims mismatch")
	}
}

func TestJWTValidationEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		setupToken  func() string
		secret      []byte
		expectError bool
		errorType   error
	}{
		{
			name: "valid token with correct secret",
			setupToken: func() string {
				token, _ := GenerateJWT("user1", "tenant1", "test@example.com", "user", []byte("correct-secret"))
				return token
			},
			secret:      []byte("correct-secret"),
			expectError: false,
		},
		{
			name: "valid token with wrong secret",
			setupToken: func() string {
				token, _ := GenerateJWT("user1", "tenant1", "test@example.com", "user", []byte("correct-secret"))
				return token
			},
			secret:      []byte("wrong-secret"),
			expectError: true,
			errorType:   ErrInvalidJWT,
		},
		{
			name: "expired token",
			setupToken: func() string {
				// Create expired token
				claims := &Claims{
					UserID:   "user1",
					TenantID: "tenant1",
					Email:    "test@example.com",
					Role:     "user",
					RegisteredClaims: jwt.RegisteredClaims{
						ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // Expired 1 hour ago
						IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
					},
				}
				token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
				tokenString, _ := token.SignedString([]byte("test-secret"))
				return tokenString
			},
			secret:      []byte("test-secret"),
			expectError: true,
			errorType:   ErrExpiredJWT,
		},
		{
			name: "malformed token",
			setupToken: func() string {
				return "not.a.valid.jwt.token"
			},
			secret:      []byte("test-secret"),
			expectError: true,
			errorType:   ErrInvalidJWT,
		},
		{
			name: "empty token",
			setupToken: func() string {
				return ""
			},
			secret:      []byte("test-secret"),
			expectError: true,
			errorType:   ErrInvalidJWT,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := tt.setupToken()
			claims, err := ValidateJWT(token, tt.secret)

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				if tt.errorType != nil && !errors.Is(err, tt.errorType) {
					t.Fatalf("expected error %v but got %v", tt.errorType, err)
				}
				if claims != nil {
					t.Fatalf("expected nil claims when error occurs")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if claims == nil {
					t.Fatalf("expected valid claims")
				}
			}
		})
	}
}

func TestJWTAlgorithmConfusionPrevention(t *testing.T) {
	// Test that we reject tokens using different signing methods
	secret := []byte("test-secret")

	// Create a token using none algorithm (security vulnerability if not caught)
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{
		UserID:   "user1",
		TenantID: "tenant1",
		Email:    "test@example.com",
		Role:     "admin",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
	noneTokenString, err := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("failed to create none token: %v", err)
	}

	// Our validation should reject this
	claims, err := ValidateJWT(noneTokenString, secret)
	if err == nil {
		t.Fatalf("expected rejection of none algorithm token but validation succeeded")
	}
	if claims != nil {
		t.Fatalf("expected nil claims when rejecting none algorithm")
	}
	// The error should be either invalid JWT or unexpected signing method
	if !errors.Is(err, ErrInvalidJWT) && !strings.Contains(err.Error(), "unexpected signing method") {
		t.Fatalf("expected signing method or invalid JWT error but got: %v", err)
	}
}

func TestJWTClaimsValidation(t *testing.T) {
	secret := []byte("test-secret")

	tests := []struct {
		name     string
		userID   string
		tenantID string
		email    string
		role     string
	}{
		{"valid admin claims", "user123", "tenant456", "admin@example.com", "admin"},
		{"valid user claims", "user789", "tenant123", "user@example.com", "user"},
		{"empty user ID", "", "tenant123", "test@example.com", "user"},
		{"empty tenant ID", "user123", "", "test@example.com", "user"},
		{"empty email", "user123", "tenant123", "", "user"},
		{"empty role", "user123", "tenant123", "test@example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Generate and validate token
			token, err := GenerateJWT(tt.userID, tt.tenantID, tt.email, tt.role, secret)
			if err != nil {
				t.Fatalf("failed to generate JWT: %v", err)
			}

			claims, err := ValidateJWT(token, secret)
			if err != nil {
				t.Fatalf("failed to validate JWT: %v", err)
			}

			// Verify all claims are preserved correctly
			if claims.UserID != tt.userID {
				t.Errorf("expected UserID %q but got %q", tt.userID, claims.UserID)
			}
			if claims.TenantID != tt.tenantID {
				t.Errorf("expected TenantID %q but got %q", tt.tenantID, claims.TenantID)
			}
			if claims.Email != tt.email {
				t.Errorf("expected Email %q but got %q", tt.email, claims.Email)
			}
			if claims.Role != tt.role {
				t.Errorf("expected Role %q but got %q", tt.role, claims.Role)
			}

			// Verify token has proper timestamps
			if claims.IssuedAt == nil {
				t.Error("expected IssuedAt to be set")
			}
			if claims.ExpiresAt == nil {
				t.Error("expected ExpiresAt to be set")
			}
			if claims.ExpiresAt.Before(claims.IssuedAt.Time) {
				t.Error("expected ExpiresAt to be after IssuedAt")
			}
		})
	}
}
