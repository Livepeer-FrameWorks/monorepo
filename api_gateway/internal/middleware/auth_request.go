package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/internal/attribution"
	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type AuthResult struct {
	UserID        string
	TenantID      string
	Email         string
	Role          string
	AuthType      string
	TokenID       string
	JWTToken      string
	APIToken      string
	WalletAddress string
	ExpiresAt     *time.Time
	Permissions   []string
	// PlatformOperator is the platform staff grant, carried from the verified
	// token (roles claim) or the validated credential response.
	PlatformOperator bool
}

type AuthOptions struct {
	AllowCookies bool
	AllowWallet  bool
}

// AuthenticateRequest validates wallet headers or bearer tokens and returns auth context.
// Returns (nil, nil) if no auth was provided.
func AuthenticateRequest(ctx context.Context, r *http.Request, clients *clients.ServiceClients, jwtSecret []byte, opts AuthOptions, logger logging.Logger) (*AuthResult, error) {
	if r == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if opts.AllowWallet && r.Header.Get("X-Wallet-Address") != "" {
		return authenticateWallet(ctx, r, clients, logger)
	}

	token, _ := bearerToken(r.Header.Get("Authorization"))

	if token == "" && opts.AllowCookies {
		if cookieToken, err := r.Cookie("access_token"); err == nil && cookieToken != nil && cookieToken.Value != "" {
			token = cookieToken.Value
		}
	}

	if token == "" {
		return nil, nil
	}

	return AuthenticateBearerToken(ctx, token, clients, jwtSecret)
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// value. ok is false when the value is not exactly that shape.
func bearerToken(authHeader string) (string, bool) {
	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != "Bearer" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// authenticateWallet validates the X-Wallet-* headers of r through Commodore.
func authenticateWallet(ctx context.Context, r *http.Request, clients *clients.ServiceClients, logger logging.Logger) (*AuthResult, error) {
	walletAddr := r.Header.Get("X-Wallet-Address")
	signature := r.Header.Get("X-Wallet-Signature")
	message := r.Header.Get("X-Wallet-Message")
	if signature == "" || message == "" {
		return nil, fmt.Errorf("missing wallet auth headers")
	}

	attr := attribution.FromRequest(r, "wallet", "wallet_ethereum")
	resp, err := clients.Commodore.WalletLogin(ctx, walletAddr, message, signature, attr)
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("Wallet auth failed")
		}
		return nil, fmt.Errorf("wallet auth failed")
	}
	if resp == nil || resp.User == nil {
		return nil, fmt.Errorf("wallet auth returned no user")
	}
	var expiresAt *time.Time
	if resp.ExpiresAt != nil {
		value := resp.ExpiresAt.AsTime()
		expiresAt = &value
	}

	email := ""
	if resp.User.Email != nil {
		email = *resp.User.Email
	}
	return &AuthResult{
		UserID:           resp.User.Id,
		TenantID:         resp.User.TenantId,
		Email:            email,
		Role:             resp.User.Role,
		AuthType:         "wallet",
		JWTToken:         resp.Token,
		WalletAddress:    walletAddr,
		PlatformOperator: resp.User.PlatformOperator,
		ExpiresAt:        expiresAt,
	}, nil
}

var (
	// ErrInvalidCredential means the token was checked and does not
	// authenticate: it is expired, revoked, malformed, or unknown.
	ErrInvalidCredential = errors.New("invalid token")
	// ErrAuthBackendUnavailable means the token could not be checked because
	// the service that validates it failed. The credential may be valid; the
	// caller should retry instead of treating it as rejected.
	ErrAuthBackendUnavailable = errors.New("authentication backend unavailable")
)

// AuthenticateBearerToken validates an interactive JWT or, failing that, a
// developer API token. A token that does not authenticate returns an error
// wrapping ErrInvalidCredential; a failure of the API-token check itself
// returns one wrapping ErrAuthBackendUnavailable.
func AuthenticateBearerToken(ctx context.Context, token string, clients *clients.ServiceClients, jwtSecret []byte) (*AuthResult, error) {
	claims, err := auth.ValidateInteractiveJWT(token, jwtSecret)
	if err == nil {
		return &AuthResult{
			UserID:           claims.UserID,
			TenantID:         claims.TenantID,
			Email:            claims.Email,
			Role:             claims.Role,
			AuthType:         "jwt",
			JWTToken:         token,
			PlatformOperator: claims.HasRole(auth.RolePlatformOperator),
		}, nil
	}
	if errors.Is(err, auth.ErrDelegatedJWT) {
		return nil, fmt.Errorf("%w: internal delegated token is not an ingress credential", ErrInvalidCredential)
	}
	// A correctly signed JWT past its expiry is never an API token, so it is
	// rejected without asking Commodore.
	if errors.Is(err, auth.ErrExpiredJWT) {
		return nil, fmt.Errorf("%w: %w", ErrInvalidCredential, err)
	}

	if clients == nil || clients.Commodore == nil {
		return nil, ErrInvalidCredential
	}
	resp, err := clients.Commodore.ValidateAPIToken(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthBackendUnavailable, err)
	}
	if resp != nil && resp.Valid {
		// Platform-operator authority is deliberately NOT inherited by API
		// tokens: a long-lived, scope-limited token must not become
		// cross-tenant platform-admin just because its owner is staff. The
		// /admin surface is interactive (JWT); operator power requires an
		// interactive session, not a programmatic credential. (resp carries
		// the owner's platform_operator for information, but the gate ignores
		// it on this path.)
		return &AuthResult{
			UserID:      resp.UserId,
			TenantID:    resp.TenantId,
			Email:       resp.Email,
			Role:        resp.Role,
			TokenID:     resp.TokenId,
			AuthType:    "api_token",
			APIToken:    token,
			Permissions: resp.Permissions,
		}, nil
	}

	return nil, ErrInvalidCredential
}

// ApplyAuthToContext injects auth values into a context for downstream handlers.
func ApplyAuthToContext(ctx context.Context, auth *AuthResult) context.Context {
	if auth == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, auth.UserID)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, auth.TenantID)
	ctx = context.WithValue(ctx, ctxkeys.KeyEmail, auth.Email)
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, auth.Role)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, auth.AuthType)
	if auth.PlatformOperator {
		ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	}
	if auth.JWTToken != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, auth.JWTToken)
	}
	if auth.ExpiresAt != nil {
		ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, *auth.ExpiresAt)
	}
	if auth.APIToken != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyAPIToken, auth.APIToken)
	}
	if auth.TokenID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, auth.TokenID)
	}
	if auth.WalletAddress != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyWalletAddr, auth.WalletAddress)
	}
	if auth.AuthType == "api_token" {
		tokenID := auth.TokenID
		if tokenID == "" {
			tokenID = auth.APIToken
		}
		ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenHash, hashIdentifier(tokenID))
		if len(auth.Permissions) > 0 {
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, auth.Permissions)
		}
	}
	if auth.UserID != "" && auth.TenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyUser, &UserContext{
			UserID:           auth.UserID,
			TenantID:         auth.TenantID,
			Email:            auth.Email,
			Role:             auth.Role,
			TokenID:          auth.TokenID,
			Permissions:      auth.Permissions,
			PlatformOperator: auth.PlatformOperator,
		})
	}
	return ctx
}
