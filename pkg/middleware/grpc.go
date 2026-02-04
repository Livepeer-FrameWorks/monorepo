package middleware

import (
	"context"
	"crypto/subtle"
	"os"
	"strings"

	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"

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
	// MetadataPolicy controls how service-token metadata is handled.
	MetadataPolicy ServiceTokenMetadataPolicy
}

// ServiceTokenMetadataPolicy controls service-token metadata behavior.
type ServiceTokenMetadataPolicy int

const (
	MetadataPolicyUnset ServiceTokenMetadataPolicy = iota
	MetadataPolicyAllow
	MetadataPolicyAudit
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

	policy := cfg.MetadataPolicy
	if policy == MetadataPolicyUnset {
		policy = parseMetadataPolicy(os.Getenv("GRPC_METADATA_POLICY"))
	}

	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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
				// JWT is valid - add claims to context
				ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.UserID)
				ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, claims.TenantID)
				ctx = context.WithValue(ctx, ctxkeys.KeyRole, claims.Role)
				ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, token)

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

// extractMetadataToContext extracts tenant_id and user_id from gRPC metadata
// and adds them to the Go context (for service-to-service calls where
// the upstream service already validated the user).
func extractMetadataToContext(ctx context.Context, md metadata.MD, policy ServiceTokenMetadataPolicy, logger logging.Logger, method string) context.Context {
	userID := firstMetadataValue(md.Get("x-user-id"))
	tenantID := firstMetadataValue(md.Get("x-tenant-id"))

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

// IsServiceCall returns true if this is a service-to-service call (no user_id)
func IsServiceCall(ctx context.Context) bool {
	return ctxkeys.GetUserID(ctx) == "" && ctxkeys.GetTenantID(ctx) == ""
}

// GRPCLoggingInterceptor returns a unary server interceptor for request logging.
// This is a basic logging interceptor that doesn't require authentication.
func GRPCLoggingInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
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
