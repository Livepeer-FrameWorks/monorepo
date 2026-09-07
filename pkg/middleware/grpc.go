package middleware

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCAuthConfig configures the gRPC authentication interceptor
type GRPCAuthConfig struct {
	// ServiceToken is the expected SERVICE_TOKEN for service-to-service calls
	ServiceToken string
	// JWTSecret is the secret for validating user JWTs (as bytes)
	JWTSecret []byte
	// Logger for auth events
	Logger logging.Logger
	// SkipMethods is a list of method names to skip auth (e.g., health checks)
	SkipMethods []string
	// ServiceOnlyMethods are rejected for JWT callers at the transport boundary.
	// Handler-level guards remain defense in depth.
	ServiceOnlyMethods []string
	// DelegatedJWTAudience, when set, is required on API-token delegation
	// assertions. Interactive session JWTs do not carry this claim.
	DelegatedJWTAudience string
	// MetadataPolicy controls how service-token metadata is handled.
	MetadataPolicy ServiceTokenMetadataPolicy
}

// ServiceTokenMetadataPolicy controls service-token metadata behavior.
type ServiceTokenMetadataPolicy int

const (
	MetadataPolicyUnset ServiceTokenMetadataPolicy = iota
	MetadataPolicyAllow
	MetadataPolicyAudit
	MetadataPolicyDeny
)

// GRPCAuthInterceptor returns a unary server interceptor that validates authentication.
// It accepts either:
//   - A valid SERVICE_TOKEN (for service-to-service calls)
//   - A valid JWT token (for user-initiated calls)
//
// The interceptor extracts tenant_id and user_id from validated tokens and adds them
// to the context for downstream handlers.
func GRPCAuthInterceptor(cfg GRPCAuthConfig) grpc.UnaryServerInterceptor {
	skipMap := make(map[string]bool)
	for _, m := range cfg.SkipMethods {
		skipMap[m] = true
	}
	serviceOnlyMap := make(map[string]bool)
	for _, m := range cfg.ServiceOnlyMethods {
		serviceOnlyMap[m] = true
	}

	policy := cfg.MetadataPolicy
	if policy == MetadataPolicyUnset {
		policy = parseMetadataPolicy(os.Getenv("GRPC_METADATA_POLICY"))
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		// Skip auth for certain methods (health checks, etc.)
		if skipMap[info.FullMethod] {
			return handler(ctx, req)
		}

		// Extract metadata from incoming context
		md, ok := metadata.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}
		ctx = applyDemoModeMetadata(ctx, md)

		// Get authorization header
		authHeaders := md.Get("authorization")
		if len(authHeaders) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization")
		}

		authHeader := authHeaders[0]
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return nil, status.Error(codes.Unauthenticated, "invalid authorization format")
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		// Try SERVICE_TOKEN first (constant-time comparison for security)
		if cfg.ServiceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(cfg.ServiceToken)) == 1 {
			// Service token is valid - extract tenant/user from metadata if present
			ctx = extractMetadataToContext(ctx, md, policy, cfg.Logger, info.FullMethod)
			ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "service")

			if cfg.Logger != nil {
				cfg.Logger.WithFields(logging.Fields{
					"method":    info.FullMethod,
					"auth_type": "service_token",
				}).Debug("gRPC auth: service token validated")
			}

			return handler(ctx, req)
		}

		// Try JWT token
		if len(cfg.JWTSecret) > 0 {
			claims, err := auth.ValidateJWT(token, cfg.JWTSecret)
			if err == nil {
				if err := validateDelegatedJWT(claims, cfg.DelegatedJWTAudience); err != nil {
					return nil, err
				}
				if serviceOnlyMap[info.FullMethod] {
					return nil, status.Error(codes.PermissionDenied, "method requires service authentication")
				}
				// JWT is valid - add claims to context
				ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
				ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, claims.Role)
				if claims.HasRole(auth.RolePlatformOperator) {
					ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
				ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, claims.ID)
				if claims.ExpiresAt != nil {
					ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, claims.ExpiresAt.Time)
				}
				authType := claims.AuthType
				if authType == "" {
					authType = "jwt"
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, authType)
				if authType == "api_token" {
					ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, claims.TokenID)
				}
				if len(claims.Permissions) > 0 {
					ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, claims.Permissions)
				}

				if cfg.Logger != nil {
					cfg.Logger.WithFields(logging.Fields{
						"method":    info.FullMethod,
						"auth_type": "jwt",
						"user_id":   claims.UserID,
						"tenant_id": claims.TenantID,
					}).Debug("gRPC auth: JWT validated")
				}

				return handler(ctx, req)
			}
		}

		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}
}

type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// GRPCStreamAuthInterceptor returns a streaming server interceptor that validates authentication.
// It accepts either:
//   - A valid SERVICE_TOKEN (for service-to-service calls)
//   - A valid JWT token (for user-initiated calls)
//
// The interceptor extracts tenant_id and user_id from validated tokens and adds them
// to the context for downstream handlers.
func GRPCStreamAuthInterceptor(cfg GRPCAuthConfig) grpc.StreamServerInterceptor {
	skipMap := make(map[string]bool)
	for _, m := range cfg.SkipMethods {
		skipMap[m] = true
	}
	serviceOnlyMap := make(map[string]bool)
	for _, m := range cfg.ServiceOnlyMethods {
		serviceOnlyMap[m] = true
	}

	policy := cfg.MetadataPolicy
	if policy == MetadataPolicyUnset {
		policy = parseMetadataPolicy(os.Getenv("GRPC_METADATA_POLICY"))
	}

	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if skipMap[info.FullMethod] {
			return handler(srv, stream)
		}

		md, ok := metadata.FromIncomingContext(stream.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}

		ctx := applyDemoModeMetadata(stream.Context(), md)

		authHeaders := md.Get("authorization")
		if len(authHeaders) == 0 {
			return status.Error(codes.Unauthenticated, "missing authorization")
		}

		authHeader := authHeaders[0]
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return status.Error(codes.Unauthenticated, "invalid authorization format")
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")

		if cfg.ServiceToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(cfg.ServiceToken)) == 1 {
			ctx = extractMetadataToContext(ctx, md, policy, cfg.Logger, info.FullMethod)
			ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
			if cfg.Logger != nil {
				cfg.Logger.WithFields(logging.Fields{
					"method":    info.FullMethod,
					"auth_type": "service_token",
				}).Debug("gRPC auth: service token validated")
			}

			return handler(srv, &wrappedServerStream{ServerStream: stream, ctx: ctx})
		}

		if len(cfg.JWTSecret) > 0 {
			claims, err := auth.ValidateJWT(token, cfg.JWTSecret)
			if err == nil {
				if err := validateDelegatedJWT(claims, cfg.DelegatedJWTAudience); err != nil {
					return err
				}
				if serviceOnlyMap[info.FullMethod] {
					return status.Error(codes.PermissionDenied, "method requires service authentication")
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
				ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, claims.Role)
				if claims.HasRole(auth.RolePlatformOperator) {
					ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)
				ctx = context.WithValue(ctx, ctxkeys.KeyJWTID, claims.ID)
				if claims.ExpiresAt != nil {
					ctx = context.WithValue(ctx, ctxkeys.KeyJWTExpiresAt, claims.ExpiresAt.Time)
				}
				authType := claims.AuthType
				if authType == "" {
					authType = "jwt"
				}
				ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, authType)
				if authType == "api_token" {
					ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, claims.TokenID)
				}
				if len(claims.Permissions) > 0 {
					ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, claims.Permissions)
				}

				if cfg.Logger != nil {
					cfg.Logger.WithFields(logging.Fields{
						"method":    info.FullMethod,
						"auth_type": "jwt",
						"user_id":   claims.UserID,
						"tenant_id": claims.TenantID,
					}).Debug("gRPC auth: JWT validated")
				}

				return handler(srv, &wrappedServerStream{ServerStream: stream, ctx: ctx})
			}
		}

		return status.Error(codes.Unauthenticated, "invalid token")
	}
}

type delegatedJWTReplayGuard struct {
	db            *sql.DB
	query         string
	pruneQuery    string
	nextPruneUnix atomic.Int64
}

func newDelegatedJWTReplayGuard(db *sql.DB, schema string) *delegatedJWTReplayGuard {
	var table string
	switch schema {
	case "commodore", "quartermaster", "purser", "periscope":
		table = schema + ".delegated_jwt_replays"
	default:
		panic("unsupported delegated JWT replay schema: " + schema)
	}
	guard := &delegatedJWTReplayGuard{
		db: db,
		query: fmt.Sprintf(`
WITH claimed AS (
	INSERT INTO %s (jti, expires_at)
	VALUES ($1, $2)
	ON CONFLICT (jti) DO NOTHING
	RETURNING 1
)
SELECT EXISTS (SELECT 1 FROM claimed)`, table),
		pruneQuery: fmt.Sprintf("DELETE FROM %s WHERE expires_at < NOW()", table),
	}
	guard.nextPruneUnix.Store(time.Now().Add(time.Minute).Unix())
	return guard
}

func (g *delegatedJWTReplayGuard) consume(ctx context.Context) error {
	if ctxkeys.GetAuthType(ctx) != "api_token" {
		return nil
	}
	jti := ctxkeys.GetJWTID(ctx)
	expiresAt, ok := ctxkeys.GetJWTExpiresAt(ctx)
	if g.db == nil || jti == "" || !ok {
		return status.Error(codes.Unauthenticated, "delegated token replay state unavailable")
	}
	nowUnix := time.Now().Unix()
	pruneAfter := g.nextPruneUnix.Load()
	if nowUnix >= pruneAfter && g.nextPruneUnix.CompareAndSwap(pruneAfter, nowUnix+60) {
		// Expiry cleanup is maintenance, not part of authentication. A cleanup
		// failure must not turn every otherwise valid API-token request into an
		// outage; the indexed rows can be reclaimed on a later interval.
		if _, pruneErr := g.db.ExecContext(ctx, g.pruneQuery); pruneErr != nil {
			g.nextPruneUnix.Store(nowUnix + 10)
		}
	}
	var claimed bool
	if err := g.db.QueryRowContext(ctx, g.query, jti, expiresAt).Scan(&claimed); err != nil {
		return status.Error(codes.Unavailable, "delegated token replay check unavailable")
	}
	if !claimed {
		return status.Error(codes.Unauthenticated, "delegated token replay rejected")
	}
	return nil
}

// DelegatedJWTReplayInterceptor atomically consumes each delegated API-token
// JTI. Callers mint one assertion per RPC attempt, so a conflict is an actual
// replay rather than another call made by the same Gateway request.
func DelegatedJWTReplayInterceptor(db *sql.DB, schema string) grpc.UnaryServerInterceptor {
	guard := newDelegatedJWTReplayGuard(db, schema)

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := guard.consume(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// DelegatedJWTStreamReplayInterceptor applies the same one-use assertion
// contract to streaming RPCs after GRPCStreamAuthInterceptor has populated the
// authenticated stream context.
func DelegatedJWTStreamReplayInterceptor(db *sql.DB, schema string) grpc.StreamServerInterceptor {
	guard := newDelegatedJWTReplayGuard(db, schema)
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := guard.consume(stream.Context()); err != nil {
			return err
		}
		return handler(srv, stream)
	}
}

func validateDelegatedJWT(claims *auth.Claims, audience string) error {
	if claims.AuthType == "" && len(claims.Audience) == 0 {
		return nil
	}
	if claims.AuthType != "api_token" || audience == "" || !slices.Contains([]string(claims.Audience), audience) {
		return status.Error(codes.Unauthenticated, "invalid delegated token audience")
	}
	if claims.TokenID == "" || claims.Subject != claims.TokenID || claims.ID == "" || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return status.Error(codes.Unauthenticated, "invalid delegated token claims")
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt.Time) > auth.DelegatedAPITokenTTL+auth.JWTClockSkewLeeway {
		return status.Error(codes.Unauthenticated, "invalid delegated token lifetime")
	}
	return nil
}

// extractMetadataToContext extracts tenant_id and user_id from gRPC metadata
// and adds them to the Go context (for service-to-service calls where
// the upstream service already validated the user).
func extractMetadataToContext(ctx context.Context, md metadata.MD, policy ServiceTokenMetadataPolicy, logger logging.Logger, method string) context.Context {
	userID := firstMetadataValue(md.Get("x-user-id"))
	tenantID := firstMetadataValue(md.Get("x-tenant-id"))
	if policy == MetadataPolicyDeny {
		if (userID != "" || tenantID != "") && logger != nil {
			logger.WithFields(logging.Fields{
				"method":          method,
				"injected_user":   userID,
				"injected_tenant": tenantID,
			}).Warn("Service token metadata injection denied")
		}
		return ctx
	}

	if policy == MetadataPolicyAudit && (userID != "" || tenantID != "") && logger != nil {
		logger.WithFields(logging.Fields{
			"method":          method,
			"injected_user":   userID,
			"injected_tenant": tenantID,
		}).Info("Service token metadata injection (audit)")
	}

	if userID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	}
	if tenantID != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	}
	return ctx
}

func applyDemoModeMetadata(ctx context.Context, md metadata.MD) context.Context {
	if value := firstMetadataValue(md.Get("x-demo-mode")); strings.EqualFold(value, "true") {
		return context.WithValue(ctx, ctxkeys.KeyDemoMode, true)
	}
	return ctx
}

func parseMetadataPolicy(value string) ServiceTokenMetadataPolicy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "audit":
		return MetadataPolicyAudit
	case "deny":
		return MetadataPolicyDeny
	case "allow", "":
		return MetadataPolicyAllow
	default:
		return MetadataPolicyAllow
	}
}

func firstMetadataValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// GetTenantID extracts tenant_id from context (set by auth middleware)
func GetTenantID(ctx context.Context) string {
	return ctxkeys.GetTenantID(ctx)
}

// GetUserID extracts user_id from context (set by auth middleware)
func GetUserID(ctx context.Context) string {
	return ctxkeys.GetUserID(ctx)
}

// IsServiceCall reports whether the authentication interceptor validated the
// configured service token. Absence of user/tenant identity is not proof of a
// service call: an empty or directly constructed context must fail closed.
func IsServiceCall(ctx context.Context) bool {
	return ctxkeys.GetAuthType(ctx) == "service"
}

// shortMethodName returns the trailing component of a gRPC FullMethod
// (e.g. "/commodore.CommodoreService/Login" to "Login"). Falls back to the
// original string when no slash is present.
func shortMethodName(fullMethod string) string {
	if idx := strings.LastIndex(fullMethod, "/"); idx >= 0 && idx < len(fullMethod)-1 {
		return fullMethod[idx+1:]
	}
	return fullMethod
}

// grpcStatusLabel maps an error returned from a gRPC handler to the status
// label written on the request counter. Nil maps to "ok"; otherwise the canonical
// gRPC code name (e.g. "Unauthenticated", "PermissionDenied"). Non-gRPC
// errors land on "Unknown".
func grpcStatusLabel(err error) string {
	if err == nil {
		return "ok"
	}
	return status.Code(err).String()
}

// GRPCMetricsInterceptor returns a unary server interceptor that records
// per-method request counts and duration. The interceptor MUST be placed
// outermost in the chain (before logging and auth); otherwise
// Unauthenticated/PermissionDenied rejections from downstream interceptors
// would be invisible to the metric, hiding exactly the failure signal we
// want to surface.
//
// requests labels: {method, status}. duration labels: {method}.
func GRPCMetricsInterceptor(requests *prometheus.CounterVec, duration *prometheus.HistogramVec) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		method := shortMethodName(info.FullMethod)
		if requests != nil {
			requests.WithLabelValues(method, grpcStatusLabel(err)).Inc()
		}
		if duration != nil {
			duration.WithLabelValues(method).Observe(time.Since(start).Seconds())
		}
		return resp, err
	}
}

// GRPCStreamMetricsInterceptor is the streaming counterpart to
// GRPCMetricsInterceptor. Same placement rule applies (outermost in the
// chain). Duration covers the full stream lifetime, from open to close.
func GRPCStreamMetricsInterceptor(requests *prometheus.CounterVec, duration *prometheus.HistogramVec) grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, stream)
		method := shortMethodName(info.FullMethod)
		if requests != nil {
			requests.WithLabelValues(method, grpcStatusLabel(err)).Inc()
		}
		if duration != nil {
			duration.WithLabelValues(method).Observe(time.Since(start).Seconds())
		}
		return err
	}
}

// GRPCLoggingInterceptor returns a unary server interceptor for request logging.
// This is a basic logging interceptor that doesn't require authentication.
func GRPCLoggingInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := ctx.Value(ctxkeys.KeyRequestStart)
		if start == nil {
			// Add start time if not present
			ctx = context.WithValue(ctx, ctxkeys.KeyRequestStart, true)
		}

		resp, err := handler(ctx, req)

		// Log after handling
		fields := logging.Fields{
			"method": info.FullMethod,
		}
		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			fields["user_id"] = userID
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			fields["tenant_id"] = tenantID
		}
		if err != nil {
			fields["error"] = err.Error()
			logger.WithFields(fields).Warn("gRPC request failed")
		} else {
			logger.WithFields(fields).Debug("gRPC request completed")
		}

		return resp, err
	}
}
