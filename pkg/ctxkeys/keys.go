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
	// KeyPlatformOperator marks the authenticated principal as platform staff
	// (the RFC 9068 platform_operator role). Set only from a verified token /
	// validated credential, never trusted across the service boundary.
	KeyPlatformOperator Key = "platform_operator"
)

// Payment context keys
const KeyXPayment Key = "x_payment"

// Request context keys
const (
	KeyServiceToken Key = "service_token"
	KeyJWTExpiresAt Key = "jwt_expires_at"
	KeyClientIP     Key = "client_ip"
	KeyRequestPath  Key = "request_path"
	KeyRequestStart Key = "request_start"
	// KeyMediaRequestRPCObserver is private-by-convention plumbing for the
	// media-cluster autonomy guard. Only WithMediaRequestRPCObserver and
	// ObserveMediaRequestRPC should access its value.
	KeyMediaRequestRPCObserver Key = "media_request_rpc_observer"
	// KeyAuthenticatedNodeCluster is set only after a node-bound balancer
	// capability has authenticated both the node and its media cluster.
	KeyAuthenticatedNodeCluster Key = "authenticated_node_cluster"
)

type mediaRequestRPCObserver struct {
	path    string
	observe func(path, service, method string)
}

// WithMediaRequestRPCObserver marks a media request path so the shared gRPC
// client interceptor can account every control-plane RPC made beneath it.
func WithMediaRequestRPCObserver(ctx context.Context, path string, observe func(path, service, method string)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, KeyMediaRequestRPCObserver, mediaRequestRPCObserver{path: path, observe: observe})
}

// ObserveMediaRequestRPC invokes the request-scoped observer, if any.
func ObserveMediaRequestRPC(ctx context.Context, service, method string) {
	if ctx == nil {
		return
	}
	observer, ok := ctx.Value(KeyMediaRequestRPCObserver).(mediaRequestRPCObserver)
	if !ok || observer.observe == nil {
		return
	}
	observer.observe(observer.path, service, method)
}

// Demo mode context keys
const (
	KeyDemoMode     Key = "demo_mode"
	KeyDemoTenantID Key = "demo_tenant_id"
	KeyDemoUserID   Key = "demo_user_id"
	KeyReadOnly     Key = "read_only"
)

// Federation context keys
const (
	KeyNoForward Key = "no_forward"
)

// Misc context keys
const (
	KeyGinContext           Key = "GinContext"
	KeyPublicAllowlisted    Key = "public_allowlisted"
	KeyLoaders              Key = "loaders"
	KeyWSCookieToken        Key = "ws_cookie_token"
	KeyHTTPRequest          Key = "http_request"
	KeyCapability           Key = "cap"
	KeyClusterScope         Key = "cluster_scope"
	KeyClusterServeScope    Key = "cluster_serve_scope"
	KeyGraphQLOperationType Key = "graphql_operation_type"
	KeyGraphQLOperationName Key = "graphql_operation_name"
	KeyGraphQLComplexity    Key = "graphql_complexity"
	KeyGraphQLErrorCount    Key = "graphql_error_count"
	// KeyPlaybackContentID carries the canonical viewer playback_id resolved from a
	// (possibly Relay/global) content_id, so the MCP access middleware and the tool
	// handler share one normalization instead of resolving twice.
	KeyPlaybackContentID Key = "playback_content_id"
)

// GetTenantID extracts tenant_id from context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(KeyTenantID).(string); ok {
		return v
	}
	return ""
}

// ClusterServeScope is the already-resolved playback authority envelope. It
// deliberately contains only value types so balancer selection never performs
// a synchronous control-plane lookup on the viewer hot path.
type ClusterServeScope struct {
	TenantID                    string
	OfficialClusterID           string
	AllowPlatformSharedPlayback bool
	PeerClusterIDs              []string
}

// GetPlaybackContentID extracts the pre-resolved canonical playback_id from context.
func GetPlaybackContentID(ctx context.Context) string {
	if v, ok := ctx.Value(KeyPlaybackContentID).(string); ok {
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

// GetClusterScope extracts the cluster scope (tenant ID) used for node isolation on shared Foghrons.
func GetClusterScope(ctx context.Context) string {
	if v, ok := ctx.Value(KeyClusterScope).(string); ok {
		return v
	}
	return ""
}

// GetClusterServeScope extracts the pre-resolved playback authority envelope.
func GetClusterServeScope(ctx context.Context) (ClusterServeScope, bool) {
	v, ok := ctx.Value(KeyClusterServeScope).(ClusterServeScope)
	return v, ok
}

// GetPermissions extracts permissions from context.
func GetPermissions(ctx context.Context) []string {
	if v, ok := ctx.Value(KeyPermissions).([]string); ok {
		return v
	}
	return nil
}

// IsPlatformOperator reports whether the context carries the platform operator
// grant. Fail-closed: absent/non-bool → false.
func IsPlatformOperator(ctx context.Context) bool {
	if v, ok := ctx.Value(KeyPlatformOperator).(bool); ok {
		return v
	}
	return false
}
