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

func TestApplyX402SessionHeadersSetsCookiesWithoutOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)

	applyX402SessionHeaders(c, &AuthResult{
		AuthType: "x402",
		JWTToken: "token",
		TenantID: "tenant",
	})

	if got := w.Header().Get("X-Access-Token"); got != "token" {
		t.Fatalf("expected X-Access-Token header, got %q", got)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 2 {
		t.Fatalf("expected x402 session cookies for non-browser/same-site flow, got %d", len(cookies))
	}
}

func TestApplyX402SessionHeadersSkipsCookiesForNonCredentialedCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)
	c.Request.Header.Set("Origin", "https://developer.example")

	applyX402SessionHeaders(c, &AuthResult{
		AuthType: "x402",
		JWTToken: "token",
		TenantID: "tenant",
	})

	if got := w.Header().Get("X-Access-Token"); got != "token" {
		t.Fatalf("expected X-Access-Token header, got %q", got)
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("expected no cookies for non-credentialed CORS flow, got %d", len(cookies))
	}
}

func TestApplyX402SessionHeadersSetsCookiesForCredentialedCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", nil)
	c.Request.Header.Set("Origin", "https://chartroom.frameworks.network")
	w.Header().Set("Access-Control-Allow-Credentials", "true")

	applyX402SessionHeaders(c, &AuthResult{
		AuthType: "x402",
		JWTToken: "token",
		TenantID: "tenant",
	})

	if cookies := w.Result().Cookies(); len(cookies) != 2 {
		t.Fatalf("expected x402 session cookies for credentialed CORS flow, got %d", len(cookies))
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

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthInvalidXPaymentReturnsPaymentRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"mutation { createStream(input: {title: \"x\"}) { __typename } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	req.Header.Set("X-PAYMENT", "not-base64")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 for invalid x402 payment, got %d", w.Code)
	}
}

func TestPublicOrJWTAuthAllowsWalletLoginMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	body := []byte(`{"query":"mutation { walletLogin(input: {address: \"0x0000000000000000000000000000000000000000\", message: \"FrameWorks Login\\nTimestamp: 2025-01-15T12:00:00Z\\nNonce: n\", signature: \"0xabc\"}) { __typename } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for public walletLogin, got %d", w.Code)
	}
}

// bootstrapEdge is the one mutation that may run without a JWT — the
// bootstrap token in the input IS the credential, validated via
// Quartermaster downstream.
func TestPublicOrJWTAuthAllowsBootstrapEdgeMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	body := []byte(`{"query":"mutation { bootstrapEdge(input: {token: \"bt_abc\"}) { ... on BootstrapEdgeResponse { nodeId } } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for public bootstrapEdge, got %d", w.Code)
	}
}

// Aliasing the public field must not change the allowlist decision —
// resolved field name (NOT alias) is what matters.
func TestPublicOrJWTAuthAliasedBootstrapEdgeStillPublic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	body := []byte(`{"query":"mutation { boot: bootstrapEdge(input: {token: \"bt_abc\"}) { ... on BootstrapEdgeResponse { nodeId } } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for aliased bootstrapEdge, got %d", w.Code)
	}
}

// Batching the public field with a private field must require auth —
// otherwise the public allowlist would let any caller smuggle private
// mutations under the same operation.
func TestPublicOrJWTAuthRejectsBatchedPublicAndPrivateMutation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	body := []byte(`{"query":"mutation { bootstrapEdge(input: {token: \"bt_abc\"}) { __typename } updateStream(id: \"1\") { id } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402 for batched public+private mutation, got %d", w.Code)
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

func TestPublicOrJWTAuthRejectsMixedAllowlistedQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(PublicOrJWTAuth([]byte("secret"), &clients.ServiceClients{}))
	r.POST("/graphql", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	body := []byte(`{"query":"query { resolveViewerEndpoint(contentId:\"abc\") { streamName } me { id } }"}`)
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "/graphql", bytes.NewReader(body))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", w.Code)
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
