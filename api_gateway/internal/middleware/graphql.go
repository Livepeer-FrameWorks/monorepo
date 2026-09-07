package middleware

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/loaders"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/gin-gonic/gin"
)

// UserContext represents authenticated user information for GraphQL resolvers
type UserContext struct {
	UserID           string
	TenantID         string
	Email            string
	Role             string
	TokenID          string
	Permissions      []string
	PlatformOperator bool
}

// GraphQLContextMiddleware transfers user info from Gin context to request context
// for GraphQL resolvers. Auth is already handled by PublicOrJWTAuth middleware.
func GraphQLContextMiddleware(expectedServiceToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()

		// Inject Gin context for resolvers that need direct access (e.g. for ClientIP)
		ctx = context.WithValue(ctx, ctxkeys.KeyGinContext, c)

		// Validate service token from Authorization header
		if ctxkeys.GetServiceToken(ctx) == "" {
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" {
				parts := strings.Split(authHeader, " ")
				if len(parts) == 2 && parts[0] == "Service" {
					if auth.ValidateServiceToken(parts[1], expectedServiceToken) == nil {
						ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, parts[1])
					}
				}
			}
		}

		// Get user info from Gin context (set by PublicOrJWTAuth)
		// or from request context (for WebSocket connections)
		var userIDStr, tenantIDStr, emailStr, roleStr string
		var permissions []string
		var platformOperator bool
		var authenticated bool

		// Try Gin context first (HTTP requests)
		if userIDVal, exists := c.Get(string(ctxkeys.KeyUserID)); exists {
			if userIDStr, authenticated = userIDVal.(string); authenticated {
				if v, ok := c.Get(string(ctxkeys.KeyTenantID)); ok {
					tenantIDStr, _ = v.(string)
				}
				if v, ok := c.Get(string(ctxkeys.KeyEmail)); ok {
					emailStr, _ = v.(string)
				}
				if v, ok := c.Get(string(ctxkeys.KeyRole)); ok {
					roleStr, _ = v.(string)
				}
				if v, ok := c.Get(string(ctxkeys.KeyPermissions)); ok {
					permissions, _ = v.([]string)
				}
				if v, ok := c.Get(string(ctxkeys.KeyPlatformOperator)); ok {
					if b, isBool := v.(bool); isBool {
						platformOperator = b
					}
				}
			}
		}

		// Fall back to request context (WebSocket connections)
		if !authenticated {
			if userIDVal := ctx.Value(ctxkeys.KeyUserID); userIDVal != nil {
				if userIDStr, authenticated = userIDVal.(string); authenticated {
					tenantIDStr = ctxkeys.GetTenantID(ctx)
					emailStr = ctxkeys.GetEmail(ctx)
					roleStr = ctxkeys.GetRole(ctx)
					permissions = ctxkeys.GetPermissions(ctx)
					platformOperator = ctxkeys.IsPlatformOperator(ctx)
				}
			}
		}

		// Build user context for GraphQL resolvers
		if authenticated && userIDStr != "" && tenantIDStr != "" {
			user := &UserContext{
				UserID:           userIDStr,
				TenantID:         tenantIDStr,
				Email:            emailStr,
				Role:             roleStr,
				Permissions:      permissions,
				PlatformOperator: platformOperator,
			}
			ctx = context.WithValue(ctx, ctxkeys.KeyUser, user)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, userIDStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantIDStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyEmail, emailStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyRole, roleStr)
			ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, platformOperator)
		}

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GraphQLAttachLoaders attaches per-request dataloaders to the context
func GraphQLAttachLoaders(sc *clients.ServiceClients) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		lds := loaders.New(sc)
		ctx = loaders.ContextWithLoaders(ctx, lds)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// GetUserFromContext extracts user information from GraphQL resolver context
func GetUserFromContext(ctx context.Context) *UserContext {
	if v := ctx.Value(ctxkeys.KeyUser); v != nil {
		if user, ok := v.(*UserContext); ok {
			return user
		}
	}
	return nil
}

// RequireAuth checks if user is authenticated and returns error if not
func RequireAuth(ctx context.Context) (*UserContext, error) {
	user := GetUserFromContext(ctx)
	if user == nil {
		return nil, auth.ErrUnauthenticated
	}
	return user, nil
}

var ErrForbidden = errors.New("insufficient permissions")

// RequirePermission checks if the current request has a specific permission.
func RequirePermission(ctx context.Context, permission string) error {
	if permission == "" {
		return nil
	}

	if HasServiceToken(ctx) {
		return nil
	}

	if ctxkeys.IsPublicAllowlisted(ctx) {
		return nil
	}

	switch ctxkeys.GetAuthType(ctx) {
	case "jwt", "wallet":
		return nil
	case "api_token":
		for _, perm := range ctxkeys.GetPermissions(ctx) {
			if strings.TrimSpace(perm) == permission {
				return nil
			}
		}
		return fmt.Errorf("%w: requires %s scope", ErrForbidden, permission)
	default:
		return auth.ErrUnauthenticated
	}
}

// HasPermission returns true if the current context has the permission.
func HasPermission(ctx context.Context, permission string) bool {
	return RequirePermission(ctx, permission) == nil
}

// RequireTenantAction combines API-token scope enforcement with the
// resource-aware owner/admin policy used by JWT, wallet, and API-token actors.
// RequirePermission alone intentionally treats session JWTs as fully scoped,
// so it is not sufficient for private infrastructure lifecycle operations.
func RequireTenantAction(ctx context.Context, permission string, action authz.Action, ownerTenantID string) error {
	if HasServiceToken(ctx) || ctxkeys.GetAuthType(ctx) == "service" {
		return nil
	}
	if err := RequirePermission(ctx, permission); err != nil {
		return err
	}
	id := authz.Identity{
		UserID:           ctxkeys.GetUserID(ctx),
		TenantID:         ctxkeys.GetTenantID(ctx),
		Role:             ctxkeys.GetRole(ctx),
		Permissions:      ctxkeys.GetPermissions(ctx),
		PlatformOperator: ctxkeys.IsPlatformOperator(ctx),
	}
	if user := GetUserFromContext(ctx); user != nil {
		id.UserID = user.UserID
		id.TenantID = user.TenantID
		id.Role = user.Role
		id.Permissions = user.Permissions
		id.PlatformOperator = user.PlatformOperator
	}
	if strings.TrimSpace(id.TenantID) == "" && !id.PlatformOperator {
		return auth.ErrUnauthenticated
	}
	decision := authz.Default.Can(ctx, id, action, authz.Resource{OwnerTenantID: ownerTenantID})
	if !decision.Allow {
		return fmt.Errorf("%w: %s", ErrForbidden, decision.Reason)
	}
	return nil
}

// RequireX402Target applies the viewer-pays boundary consistently across API
// entry points. Viewer resources may be funded by any authenticated caller
// holding the billing scope; private resources still require authority over
// the tenant that owns the resolved target. Public playback middleware may
// explicitly admit an anonymous viewer payment.
func RequireX402Target(ctx context.Context, resourceKind, targetTenantID string, allowAnonymousViewer bool) error {
	if strings.EqualFold(strings.TrimSpace(resourceKind), "viewer") {
		if allowAnonymousViewer && strings.TrimSpace(ctxkeys.GetAuthType(ctx)) == "" {
			return nil
		}
		return RequirePermission(ctx, "billing:write")
	}
	return RequireTenantAction(ctx, "billing:write", authz.ActionManageBilling, targetTenantID)
}

// HasServiceToken checks if the current context has a service token
func HasServiceToken(ctx context.Context) bool {
	return ctxkeys.GetServiceToken(ctx) != ""
}
