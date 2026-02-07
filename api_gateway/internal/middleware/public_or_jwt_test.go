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

	body := []byte("query { serviceInstancesHealth }")
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
