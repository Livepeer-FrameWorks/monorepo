// Package ctxkeys defines typed context keys to avoid SA1029 lint warnings
// and prevent key collisions across packages.
package ctxkeys

import (
	"context"
	"time"
)

// Key is a typed context key to prevent collisions.
type Key string

// Auth context keys
const (
	KeyUserID       Key = "user_id"
	KeyTenantID     Key = "tenant_id"
	KeyEmail        Key = "email"
	KeyRole         Key = "role"
	KeyJWTToken     Key = "jwt_token"
	KeyAPIToken     Key = "api_token"
	KeyAPITokenHash Key = "api_token_hash"
	KeyUser         Key = "user"
	KeyAuthType     Key = "auth_type"
	KeySessionToken Key = "session_token"
	KeyWalletAddr   Key = "wallet_address"
	KeyPermissions  Key = "permissions"
)

// X402 context keys
const (
	KeyX402Processed Key = "x402_processed"
	KeyX402AuthOnly  Key = "x402_auth_only"
	KeyXPayment      Key = "x_payment"
)

// Request context keys
const (
	KeyServiceToken Key = "service_token"
	KeyJWTExpiresAt Key = "jwt_expires_at"
	KeyClientIP     Key = "client_ip"
	KeyRequestPath  Key = "request_path"
	KeyRequestStart Key = "request_start"
)

// Demo mode context keys
const (
	KeyDemoMode     Key = "demo_mode"
	KeyDemoTenantID Key = "demo_tenant_id"
	KeyDemoUserID   Key = "demo_user_id"
	KeyReadOnly     Key = "read_only"
)

// Misc context keys
const (
	KeyGinContext        Key = "GinContext"
	KeyPublicAllowlisted Key = "public_allowlisted"
	KeyLoaders           Key = "loaders"
	KeyWSCookieToken     Key = "ws_cookie_token"
	KeyHTTPRequest       Key = "http_request"
	KeyCapability        Key = "cap"
)

// GetTenantID extracts tenant_id from context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(KeyTenantID).(string); ok {
		return v
	}
	return ""
}

// GetUserID extracts user_id from context.
func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(KeyUserID).(string); ok {
		return v
	}
	return ""
}

// GetEmail extracts email from context.
func GetEmail(ctx context.Context) string {
	if v, ok := ctx.Value(KeyEmail).(string); ok {
		return v
	}
	return ""
}

// GetRole extracts role from context.
func GetRole(ctx context.Context) string {
	if v, ok := ctx.Value(KeyRole).(string); ok {
		return v
	}
	return ""
}

// GetJWTToken extracts jwt_token from context.
func GetJWTToken(ctx context.Context) string {
	if v, ok := ctx.Value(KeyJWTToken).(string); ok {
		return v
	}
	return ""
}

// GetAPIToken extracts api_token from context.
func GetAPIToken(ctx context.Context) string {
	if v, ok := ctx.Value(KeyAPIToken).(string); ok {
		return v
	}
	return ""
}

// GetAuthType extracts auth_type from context.
func GetAuthType(ctx context.Context) string {
	if v, ok := ctx.Value(KeyAuthType).(string); ok {
		return v
	}
	return ""
}

// GetServiceToken extracts service_token from context.
func GetServiceToken(ctx context.Context) string {
	if v, ok := ctx.Value(KeyServiceToken).(string); ok {
		return v
	}
	return ""
}

// GetClientIP extracts client_ip from context.
func GetClientIP(ctx context.Context) string {
	if v, ok := ctx.Value(KeyClientIP).(string); ok {
		return v
	}
	return ""
}

// GetWalletAddress extracts wallet_address from context.
func GetWalletAddress(ctx context.Context) string {
	if v, ok := ctx.Value(KeyWalletAddr).(string); ok {
		return v
	}
	return ""
}

// GetJWTExpiresAt extracts jwt_expires_at from context.
func GetJWTExpiresAt(ctx context.Context) (time.Time, bool) {
	if v, ok := ctx.Value(KeyJWTExpiresAt).(time.Time); ok {
		return v, true
	}
	return time.Time{}, false
}

// IsDemoMode checks if demo_mode is set in context.
func IsDemoMode(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyDemoMode).(bool); ok {
		return v
	}
	return false
}

// IsX402Processed checks if x402_processed is set in context.
func IsX402Processed(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyX402Processed).(bool); ok {
		return v
	}
	return false
}

// IsX402AuthOnly checks if x402_auth_only is set in context.
func IsX402AuthOnly(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyX402AuthOnly).(bool); ok {
		return v
	}
	return false
}

// IsPublicAllowlisted checks if public_allowlisted is set in context.
func IsPublicAllowlisted(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyPublicAllowlisted).(bool); ok {
		return v
	}
	return false
}

// IsReadOnly checks if read_only is set in context.
func IsReadOnly(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyReadOnly).(bool); ok {
		return v
	}
	return false
}

// GetXPayment extracts x_payment from context.
func GetXPayment(ctx context.Context) string {
	if v, ok := ctx.Value(KeyXPayment).(string); ok {
		return v
	}
	return ""
}

// GetCapability extracts capability requirement from context.
func GetCapability(ctx context.Context) string {
	if v, ok := ctx.Value(KeyCapability).(string); ok {
		return v
	}
	return ""
}

// GetPermissions extracts permissions from context.
func GetPermissions(ctx context.Context) []string {
	if v, ok := ctx.Value(KeyPermissions).([]string); ok {
		return v
	}
	return nil
}
