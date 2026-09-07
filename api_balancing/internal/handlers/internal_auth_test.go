package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/gin-gonic/gin"
)

func TestInternalOperatorIngressRejectsDelegatedAPIToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("foghorn-internal-auth-secret")
	t.Setenv("JWT_SECRET", string(secret))
	t.Setenv("SERVICE_TOKEN", "service-secret")
	delegated, err := auth.GenerateDelegatedAPITokenJWT(
		"user-1", "tenant-1", "owner@example.com", "owner", "token-1",
		[]string{"infrastructure:write"}, "foghorn", secret,
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := auth.GenerateSessionJWT(
		"operator-1", "tenant-1", "operator@example.com", "owner",
		[]string{auth.RolePlatformOperator}, time.Time{}, secret,
	)
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/internal", RequireInternalMutation(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, tc := range []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "delegated API token", token: delegated, wantStatus: http.StatusForbidden},
		{name: "interactive platform operator", token: session, wantStatus: http.StatusNoContent},
		{name: "service token cannot mutate", token: "service-secret", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/internal", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, req)
			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.Code, tc.wantStatus)
			}
		})
	}
}
