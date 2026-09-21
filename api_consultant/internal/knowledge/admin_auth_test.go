package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_consultant/internal/skipper"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

// ginRoleBridge copies the identity the auth middleware sets on the gin
// context into the request context, as Skipper's main does.
func ginRoleBridge() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := skipper.WithTenantID(c.Request.Context(), c.GetString(string(ctxkeys.KeyTenantID)))
		ctx = skipper.WithRole(ctx, c.GetString(string(ctxkeys.KeyRole)))
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// The admin routes accept the shared service token through the real JWT
// middleware, as the main /api/skipper group does, and still reject a
// request without credentials or with a wrong token.
func TestAdminRoutesAcceptServiceTokenThroughRealMiddleware(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()
	const serviceToken = "skipper-service-token"
	systemTenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	router := gin.New()
	api.RegisterRoutes(router, []byte("jwt-secret"), []auth.JWTOption{auth.WithServiceIdentity(serviceToken, systemTenantID)}, ginRoleBridge())

	get := func(authorization string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/skipper/admin/health", nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}
	if code := get("Bearer " + serviceToken); code != http.StatusOK {
		t.Fatalf("service token: status %d, want 200", code)
	}
	if code := get("Bearer not-the-token"); code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status %d, want 401", code)
	}
	if code := get(""); code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status %d, want 401", code)
	}
}
