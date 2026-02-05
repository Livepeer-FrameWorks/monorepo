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
