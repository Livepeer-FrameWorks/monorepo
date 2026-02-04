package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/pkg/ctxkeys"
	"github.com/gin-gonic/gin"
)

func TestServiceAuthMiddleware(t *testing.T) {
	r := gin.New()
	r.Use(ServiceAuthMiddleware("token123"))
	r.GET("/ok", func(c *gin.Context) { c.String(200, "ok") })

	// Missing header
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// Invalid header
	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// Valid header
	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
	req.Header.Set("Authorization", "Bearer token123")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestJWTAuthMiddleware(t *testing.T) {
	secret := []byte("secret")
	token, err := GenerateJWT("u1", "t1", "u@example.com", "admin", secret)
	if err != nil {
		t.Fatalf("GenerateJWT: %v", err)
	}

	r := gin.New()
	r.Use(JWTAuthMiddleware(secret))
	r.GET("/ok", func(c *gin.Context) {
		if c.GetString(string(ctxkeys.KeyUserID)) != "u1" || c.GetString(string(ctxkeys.KeyTenantID)) != "t1" {
			t.Fatalf("claims not set")
		}
		c.String(200, "ok")
	})

	// Missing header -> 401
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}

	// Valid token -> 200
	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}

}

func TestJWTAuthMiddleware_WebSocketUpgrade(t *testing.T) {
	secret := []byte("secret")
	r := gin.New()
	r.Use(JWTAuthMiddleware(secret))
	r.GET("/ws", func(c *gin.Context) {
		c.String(200, "ws-ok")
	})

	// WebSocket upgrade request -> allowed through without auth
	w := httptest.NewRecorder()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("WebSocket upgrade should pass without auth, got %d", w.Code)
	}

	// Only Upgrade header without Connection -> 401
	w = httptest.NewRecorder()
	req, _ = http.NewRequestWithContext(context.Background(), "GET", "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Upgrade without Connection should require auth, got %d", w.Code)
	}
}

func TestJWTAuthMiddleware_WebSocketBypass(t *testing.T) {
	secret := []byte("secret")
	r := gin.New()
	r.Use(JWTAuthMiddleware(secret))
	r.GET("/ok", func(c *gin.Context) { c.String(200, "ok") })

	tests := []struct {
		name    string
		upgrade string
		conn    string
		want    int
	}{
		{
			name:    "websocket upgrade bypasses auth",
			upgrade: "websocket",
			conn:    "Upgrade",
			want:    http.StatusOK,
		},
		{
			name:    "upgrade header only",
			upgrade: "websocket",
			conn:    "",
			want:    http.StatusUnauthorized,
		},
		{
			name:    "connection header only",
			upgrade: "",
			conn:    "Upgrade",
			want:    http.StatusUnauthorized,
		},
		{
			name:    "http2 upgrade not allowed",
			upgrade: "h2c",
			conn:    "Upgrade",
			want:    http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequestWithContext(context.Background(), "GET", "/ok", nil)
			if tt.upgrade != "" {
				req.Header.Set("Upgrade", tt.upgrade)
			}
			if tt.conn != "" {
				req.Header.Set("Connection", tt.conn)
			}
			r.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("expected %d, got %d", tt.want, w.Code)
			}
		})
	}
}
