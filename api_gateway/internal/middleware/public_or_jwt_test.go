package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/auth"
	"frameworks/pkg/ctxkeys"

	"github.com/gin-gonic/gin"
)

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestPublicOrJWTAuthAllowlistedQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		if !c.GetBool(string(ctxkeys.KeyPublicAllowlisted)) {
			t.Fatal("expected request to be allowlisted")
		}
		if c.Request.Context().Value(ctxkeys.KeyPublicAllowlisted) != true {
			t.Fatal("expected allowlisted flag on context")
		}
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"query { serviceInstancesHealth }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthInvalidBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", io.NopCloser(errReader{}))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestApplyX402CookiesUsesCookieDomain(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Setenv("COOKIE_DOMAIN", ".example.com")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	applyX402Cookies(c, &AuthResult{
		AuthType: "x402",
		JWTToken: "token",
		TenantID: "tenant",
	})

	cookies := w.Result().Cookies()
	var accessCookie *http.Cookie
	var tenantCookie *http.Cookie
	for _, cookie := range cookies {
		switch cookie.Name {
		case "access_token":
			accessCookie = cookie
		case "tenant_id":
			tenantCookie = cookie
		}
	}

	if accessCookie == nil || tenantCookie == nil {
		t.Fatalf("expected access_token and tenant_id cookies, got %d", len(cookies))
	}
	if accessCookie.Domain != "example.com" || tenantCookie.Domain != "example.com" {
		t.Fatalf("expected cookie domain example.com, got %q and %q", accessCookie.Domain, tenantCookie.Domain)
	}
}

func TestPublicOrJWTAuthRejectsUnauthenticatedMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"mutation { updateStream(id: \"1\") { id } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthAllowlistIgnoresInvalidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"query { resolveViewerEndpoint }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer invalid-token")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthUsesJWTForProtectedQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	secret := []byte("secret")
	token, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.com", "admin", secret)
	if err != nil {
		t.Fatalf("failed to generate JWT: %v", err)
	}

	r := gin.New()
	r.Use(PublicOrJWTAuth(secret, &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		if c.GetString(string(ctxkeys.KeyUserID)) != "user-1" {
			t.Fatalf("expected user context to be set")
		}
		if c.Request.Context().Value(ctxkeys.KeyTenantID) != "tenant-1" {
			t.Fatalf("expected tenant context to be set")
		}
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"query { streamsConnection { edges { node { id } } } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthWebSocketUpgradePassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.GET("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/graphql", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
