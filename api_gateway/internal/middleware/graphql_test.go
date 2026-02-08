package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/pkg/ctxkeys"

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
			r.Use(GraphQLContextMiddleware())
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
	r.Use(GraphQLContextMiddleware())
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
