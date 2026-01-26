package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/auth"
	"frameworks/pkg/logging"
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
	X402Processed bool
	X402AuthOnly  bool
}

type AuthOptions struct {
	AllowCookies bool
	AllowWallet  bool
	AllowX402    bool
}

// AuthenticateRequest validates wallet headers or bearer tokens and returns auth context.
// Returns (nil, nil) if no auth was provided.
func AuthenticateRequest(ctx context.Context, r *http.Request, clients *clients.ServiceClients, jwtSecret []byte, opts AuthOptions, logger logging.Logger) (*AuthResult, error) {
	if r == nil {
		return nil, fmt.Errorf("request is nil")
	}

	if opts.AllowX402 {
		if xPayment := GetX402PaymentHeader(r); xPayment != "" {
			payload, err := ParseX402PaymentHeader(xPayment)
			if err != nil {
				return nil, fmt.Errorf("invalid X-PAYMENT header")
			}
			clientIP := ClientIPFromRequest(r)
			walletResp, err := clients.Commodore.WalletLoginWithX402(ctx, payload, clientIP, "")
			if err != nil {
				if logger != nil {
					logger.WithError(err).Warn("X-PAYMENT login failed")
				}
				return nil, fmt.Errorf("x402 login failed")
			}
			if walletResp == nil || walletResp.Auth == nil || walletResp.Auth.User == nil {
				return nil, fmt.Errorf("x402 auth returned no user")
			}

			email := ""
			if walletResp.Auth.User.Email != nil {
				email = *walletResp.Auth.User.Email
			}
			expiresAt := (*time.Time)(nil)
			if walletResp.Auth.ExpiresAt != nil {
				value := walletResp.Auth.ExpiresAt.AsTime()
				expiresAt = &value
			}

			walletAddress := walletResp.PayerAddress
			if walletAddress == "" && payload.GetPayload() != nil && payload.GetPayload().GetAuthorization() != nil {
				walletAddress = payload.GetPayload().GetAuthorization().GetFrom()
			}

			return &AuthResult{
				UserID:        walletResp.Auth.User.Id,
				TenantID:      walletResp.Auth.User.TenantId,
				Email:         email,
				Role:          walletResp.Auth.User.Role,
				AuthType:      "x402",
				JWTToken:      walletResp.Auth.Token,
				WalletAddress: walletAddress,
				ExpiresAt:     expiresAt,
				X402Processed: true,
				X402AuthOnly:  walletResp.IsAuthOnly,
			}, nil
		}
	}

	if opts.AllowWallet {
		walletAddr := r.Header.Get("X-Wallet-Address")
		if walletAddr != "" {
			signature := r.Header.Get("X-Wallet-Signature")
			message := r.Header.Get("X-Wallet-Message")
			if signature == "" || message == "" {
				return nil, fmt.Errorf("missing wallet auth headers")
			}

			resp, err := clients.Commodore.WalletLogin(ctx, walletAddr, message, signature)
			if err != nil {
				if logger != nil {
					logger.WithError(err).Warn("Wallet auth failed")
				}
				return nil, fmt.Errorf("wallet auth failed")
			}
			if resp == nil || resp.User == nil {
				return nil, fmt.Errorf("wallet auth returned no user")
			}

			email := ""
			if resp.User.Email != nil {
				email = *resp.User.Email
			}
			return &AuthResult{
				UserID:        resp.User.Id,
				TenantID:      resp.User.TenantId,
				Email:         email,
				Role:          resp.User.Role,
				AuthType:      "wallet",
				JWTToken:      resp.Token,
				WalletAddress: walletAddr,
			}, nil
		}
	}

	var token string
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		parts := strings.Split(authHeader, " ")
		if len(parts) == 2 && parts[0] == "Bearer" {
			token = parts[1]
		}
	}

	if token == "" && opts.AllowCookies {
		if cookieToken, err := r.Cookie("access_token"); err == nil && cookieToken != nil && cookieToken.Value != "" {
			token = cookieToken.Value
		}
	}

	if token == "" {
		return nil, nil
	}

	claims, err := auth.ValidateJWT(token, jwtSecret)
	if err == nil {
		return &AuthResult{
			UserID:   claims.UserID,
			TenantID: claims.TenantID,
			Email:    claims.Email,
			Role:     claims.Role,
			AuthType: "jwt",
			JWTToken: token,
		}, nil
	}

	resp, err := clients.Commodore.ValidateAPIToken(ctx, token)
	if err == nil && resp != nil && resp.Valid {
		return &AuthResult{
			UserID:   resp.UserId,
			TenantID: resp.TenantId,
			Email:    resp.Email,
			Role:     resp.Role,
			TokenID:  resp.TokenId,
			AuthType: "api_token",
			APIToken: token,
		}, nil
	}

	return nil, fmt.Errorf("invalid token")
}

// ApplyAuthToContext injects auth values into a context for downstream handlers.
func ApplyAuthToContext(ctx context.Context, auth *AuthResult) context.Context {
	if auth == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, "user_id", auth.UserID)
	ctx = context.WithValue(ctx, "tenant_id", auth.TenantID)
	ctx = context.WithValue(ctx, "email", auth.Email)
	ctx = context.WithValue(ctx, "role", auth.Role)
	ctx = context.WithValue(ctx, "auth_type", auth.AuthType)
	if auth.JWTToken != "" {
		ctx = context.WithValue(ctx, "jwt_token", auth.JWTToken)
	}
	if auth.ExpiresAt != nil {
		ctx = context.WithValue(ctx, "jwt_expires_at", *auth.ExpiresAt)
	}
	if auth.APIToken != "" {
		ctx = context.WithValue(ctx, "api_token", auth.APIToken)
	}
	if auth.WalletAddress != "" {
		ctx = context.WithValue(ctx, "wallet_address", auth.WalletAddress)
	}
	if auth.AuthType == "x402" {
		ctx = context.WithValue(ctx, "x402_processed", auth.X402Processed)
		ctx = context.WithValue(ctx, "x402_auth_only", auth.X402AuthOnly)
		if auth.JWTToken != "" {
			ctx = context.WithValue(ctx, "session_token", auth.JWTToken)
		}
	}
	if auth.AuthType == "api_token" {
		tokenID := auth.TokenID
		if tokenID == "" {
			tokenID = auth.APIToken
		}
		ctx = context.WithValue(ctx, "api_token_hash", hashIdentifier(tokenID))
	}
	if auth.UserID != "" && auth.TenantID != "" {
		ctx = context.WithValue(ctx, "user", &UserContext{
			UserID:   auth.UserID,
			TenantID: auth.TenantID,
			Email:    auth.Email,
			Role:     auth.Role,
			TokenID:  auth.TokenID,
		})
	}
	return ctx
}
