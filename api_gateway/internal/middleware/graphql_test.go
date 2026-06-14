package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/gin-gonic/gin"
)

func TestGraphQLContextMiddleware_GinToGoContextBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		ginSetup   func(c *gin.Context)
		wantUserID string
		wantTenant string
		wantEmail  string
		wantRole   string
	}{
		{
			name: "bridges all user fields from Gin to Go context",
			ginSetup: func(c *gin.Context) {
				c.Set(string(ctxkeys.KeyUserID), "user-123")
				c.Set(string(ctxkeys.KeyTenantID), "tenant-456")
				c.Set(string(ctxkeys.KeyEmail), "test@example.com")
				c.Set(string(ctxkeys.KeyRole), "admin")
			},
			wantUserID: "user-123",
			wantTenant: "tenant-456",
			wantEmail:  "test@example.com",
			wantRole:   "admin",
		},
		{
			name: "handles missing optional fields",
			ginSetup: func(c *gin.Context) {
				c.Set(string(ctxkeys.KeyUserID), "user-789")
				c.Set(string(ctxkeys.KeyTenantID), "tenant-xyz")
			},
			wantUserID: "user-789",
			wantTenant: "tenant-xyz",
			wantEmail:  "",
			wantRole:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedUserID, capturedTenant, capturedEmail, capturedRole string

			r := gin.New()
			// First middleware sets Gin context (simulating auth middleware)
			r.Use(func(c *gin.Context) {
				tt.ginSetup(c)
				c.Next()
			})
			// GraphQL middleware bridges to Go context
			r.Use(GraphQLContextMiddleware("test-service-token"))
			// Handler verifies Go context has the values
			r.GET("/test", func(c *gin.Context) {
				ctx := c.Request.Context()
				if v := ctx.Value(ctxkeys.KeyUserID); v != nil {
					capturedUserID, _ = v.(string)
				}
				if v := ctx.Value(ctxkeys.KeyTenantID); v != nil {
					capturedTenant, _ = v.(string)
				}
				if v := ctx.Value(ctxkeys.KeyEmail); v != nil {
					capturedEmail, _ = v.(string)
				}
				if v := ctx.Value(ctxkeys.KeyRole); v != nil {
					capturedRole, _ = v.(string)
				}
				c.String(200, "ok")
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), "GET", "/test", nil)
			r.ServeHTTP(w, req)

			if capturedUserID != tt.wantUserID {
				t.Errorf("UserID: got %q, want %q", capturedUserID, tt.wantUserID)
			}
			if capturedTenant != tt.wantTenant {
				t.Errorf("TenantID: got %q, want %q", capturedTenant, tt.wantTenant)
			}
			if capturedEmail != tt.wantEmail {
				t.Errorf("Email: got %q, want %q", capturedEmail, tt.wantEmail)
			}
			if capturedRole != tt.wantRole {
				t.Errorf("Role: got %q, want %q", capturedRole, tt.wantRole)
			}
		})
	}
}

// TestGraphQLContextMiddleware_CarriesPlatformOperator guards grant erasure:
// the middleware rebuilds KeyUser, so it must carry the platform_operator grant
// into both the UserContext (which RequirePlatformOperator reads preferentially)
// and the raw KeyPlatformOperator key — otherwise an operator's grant is erased
// on every HTTP GraphQL request and /admin denies them.
func TestGraphQLContextMiddleware_CarriesPlatformOperator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, op := range []bool{true, false} {
		name := "operator"
		if !op {
			name = "non-operator"
		}
		t.Run(name, func(t *testing.T) {
			var fromUserContext, fromRawKey bool

			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set(string(ctxkeys.KeyUserID), "user-1")
				c.Set(string(ctxkeys.KeyTenantID), "tenant-1")
				c.Set(string(ctxkeys.KeyPlatformOperator), op)
				c.Next()
			})
			r.Use(GraphQLContextMiddleware("test-service-token"))
			r.GET("/test", func(c *gin.Context) {
				ctx := c.Request.Context()
				if u := GetUserFromContext(ctx); u != nil {
					fromUserContext = u.PlatformOperator
				}
				fromRawKey = ctxkeys.IsPlatformOperator(ctx)
				c.String(200, "ok")
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), "GET", "/test", nil)
			r.ServeHTTP(w, req)

			if fromUserContext != op {
				t.Errorf("UserContext.PlatformOperator: got %v, want %v", fromUserContext, op)
			}
			if fromRawKey != op {
				t.Errorf("ctxkeys.IsPlatformOperator: got %v, want %v", fromRawKey, op)
			}
		})
	}
}

// TestPlatformOperatorSurvivesFullAuthChain drives a real roles-bearing JWT
// through the actual HTTP middleware chain — PublicOrJWTAuth →
// GraphQLContextMiddleware — and asserts the platform_operator grant reaches
// the resolver-visible UserContext. This is the end-to-end coverage the
// isolated GraphQLContextMiddleware test cannot give: it would catch a
// regression anywhere from claim decoding to the gin→context handoff.
func TestPlatformOperatorSurvivesFullAuthChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret-please-do-not-use-in-prod")

	for _, op := range []bool{true, false} {
		name := "operator-jwt"
		var roles []string
		if op {
			roles = []string{auth.RolePlatformOperator}
		} else {
			name = "plain-jwt"
		}
		t.Run(name, func(t *testing.T) {
			token, err := auth.GenerateSessionJWT("u1", "t1", "u@example.com", "owner", roles, time.Time{}, secret)
			if err != nil {
				t.Fatalf("mint: %v", err)
			}

			var seen bool
			r := gin.New()
			r.Use(PublicOrJWTAuth(secret, &clients.ServiceClients{}))
			r.Use(GraphQLContextMiddleware("svc-token"))
			r.POST("/graphql", func(c *gin.Context) {
				u := GetUserFromContext(c.Request.Context())
				if u == nil {
					t.Fatal("no user context after auth chain")
				}
				seen = u.PlatformOperator
				c.String(http.StatusOK, "ok")
			})

			body := []byte(`{"query":"query { platform { tenants { generatedAt } } }"}`)
			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			if seen != op {
				t.Fatalf("resolver-visible PlatformOperator = %v, want %v", seen, op)
			}
		})
	}
}

func TestCtxkeysStringCastConsistency(t *testing.T) {
	// Verify that ctxkeys constants have expected string values
	// This catches accidental changes to key values
	tests := []struct {
		key  ctxkeys.Key
		want string
	}{
		{ctxkeys.KeyUserID, "user_id"},
		{ctxkeys.KeyTenantID, "tenant_id"},
		{ctxkeys.KeyEmail, "email"},
		{ctxkeys.KeyRole, "role"},
		{ctxkeys.KeyJWTToken, "jwt_token"},
		{ctxkeys.KeyPermissions, "permissions"},
	}

	for _, tt := range tests {
		if string(tt.key) != tt.want {
			t.Errorf("ctxkeys.%v = %q, want %q", tt.key, string(tt.key), tt.want)
		}
	}
}

func TestGraphQLContextMiddlewareMissingTenantSkipsUserContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var userContextPresent bool

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(ctxkeys.KeyUserID), "user-123")
		c.Next()
	})
	r.Use(GraphQLContextMiddleware("test-service-token"))
	r.GET("/test", func(c *gin.Context) {
		ctx := c.Request.Context()
		if ctx.Value(ctxkeys.KeyUser) != nil {
			userContextPresent = true
		}
		c.String(200, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/test", nil)
	r.ServeHTTP(w, req)

	if userContextPresent {
		t.Fatal("expected user context to be absent when tenant ID is missing")
	}
}

func TestGraphQLContextMiddleware_ServiceTokenValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		authHeader     string
		wantHasService bool
	}{
		{
			name:           "valid service token is accepted",
			authHeader:     "Service test-service-token",
			wantHasService: true,
		},
		{
			name:           "invalid service token is rejected",
			authHeader:     "Service wrong-token",
			wantHasService: false,
		},
		{
			name:           "empty service token value is rejected",
			authHeader:     "Service ",
			wantHasService: false,
		},
		{
			name:           "bearer token does not set service token",
			authHeader:     "Bearer some-jwt",
			wantHasService: false,
		},
		{
			name:           "no auth header",
			authHeader:     "",
			wantHasService: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotHasService bool

			r := gin.New()
			r.Use(GraphQLContextMiddleware("test-service-token"))
			r.GET("/test", func(c *gin.Context) {
				ctx := c.Request.Context()
				gotHasService = HasServiceToken(ctx)
				c.String(200, "ok")
			})

			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), "GET", "/test", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			r.ServeHTTP(w, req)

			if gotHasService != tt.wantHasService {
				t.Errorf("HasServiceToken() = %v, want %v", gotHasService, tt.wantHasService)
			}
		})
	}
}
