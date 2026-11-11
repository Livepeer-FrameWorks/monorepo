package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const WalletBearerAudience = "bridge"

// GenerateWalletBearerJWT preserves MCP's short-lived bootstrap credential.
// It has no interactive session ID, refresh credential or platform staff grant.
func GenerateWalletBearerJWT(userID, tenantID, email, role, walletAddress string, secret []byte) (string, error) {
	return generateWalletJWT(userID, tenantID, email, role, walletAddress, WalletBearerAudience, time.Now().Add(SessionTokenTTL), secret)
}

// GenerateDelegatedWalletJWT bounds an internal assertion by both the wallet
// bearer's expiry and the one-minute RPC assertion lifetime.
func GenerateDelegatedWalletJWT(userID, tenantID, email, role, walletAddress, audience string, credentialExpiry time.Time, secret []byte) (string, error) {
	if credentialExpiry.IsZero() || !credentialExpiry.After(time.Now()) || audience == WalletBearerAudience {
		return "", ErrInvalidJWT
	}
	expiry := minTime(credentialExpiry, time.Now().Add(DelegatedAPITokenTTL))
	return generateWalletJWT(userID, tenantID, email, role, walletAddress, audience, expiry, secret)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func generateWalletJWT(userID, tenantID, email, role, walletAddress, audience string, expiry time.Time, secret []byte) (string, error) {
	if userID == "" || tenantID == "" || walletAddress == "" || audience == "" || len(secret) == 0 {
		return "", ErrInvalidJWT
	}
	claims := &Claims{
		UserID: userID, TenantID: tenantID, Email: email, Role: role,
		AuthType: "wallet", WalletAddress: walletAddress,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: walletAddress, Audience: jwt.ClaimStrings{audience}, ID: uuid.NewString(),
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(expiry),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ValidateWalletBearerJWT(token string, secret []byte) (*Claims, error) {
	claims, err := ValidateJWT(token, secret)
	if err != nil {
		return nil, err
	}
	if claims.AuthType != "wallet" || len(claims.Audience) != 1 || claims.Audience[0] != WalletBearerAudience {
		return nil, ErrDelegatedJWT
	}
	if claims.UserID == "" || claims.TenantID == "" || claims.WalletAddress == "" || claims.Subject != claims.WalletAddress || claims.ID == "" || claims.SessionID != "" || claims.TokenID != "" || len(claims.Roles) != 0 || claims.IssuedAt == nil || claims.ExpiresAt == nil || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > SessionTokenTTL+JWTClockSkewLeeway {
		return nil, ErrInvalidJWT
	}
	return claims, nil
}
