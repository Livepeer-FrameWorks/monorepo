package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
)

// The documented constants DemoModePostAuth seeds. These are demo-only IDs and
// must never land in the real KeyTenantID/KeyUserID slots (see the SECURITY note
// in demo.go): doing so would defeat rate limiting, which keys off an empty/public
// tenant_id.
const (
	demoTenantID = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	demoUserID   = "5eedface-5e1f-da7a-face-5e1fda7a0001"
)

// runDemoMiddleware drives DemoModePostAuth over a single request and returns the
// downstream request context so the test can inspect what was injected.
func runDemoMiddleware(t *testing.T, configure func(*http.Request)) context.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var captured context.Context
	r := gin.New()
	r.Use(DemoModePostAuth(logging.NewLogger()))
	r.GET("/test", func(c *gin.Context) {
		captured = c.Request.Context()
		c.String(http.StatusOK, "ok")
	})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/test", nil)
	configure(req)
	r.ServeHTTP(httptest.NewRecorder(), req)

	if captured == nil {
		t.Fatal("downstream handler was not reached")
	}
	return captured
}

func TestDemoModePostAuth_Triggers(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*http.Request)
	}{
		{
			name: "X-Demo-Mode header",
			configure: func(req *http.Request) {
				req.Header.Set("X-Demo-Mode", "true")
			},
		},
		{
			name: "demo query parameter",
			configure: func(req *http.Request) {
				req.URL.RawQuery = "demo=true"
			},
		},
		{
			name: "API-Explorer-Demo user agent",
			configure: func(req *http.Request) {
				req.Header.Set("User-Agent", "API-Explorer-Demo/1.0")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := runDemoMiddleware(t, tc.configure)

			if !IsDemoMode(ctx) {
				t.Fatal("expected demo mode to be active")
			}
			if got := ctx.Value(ctxkeys.KeyDemoTenantID); got != demoTenantID {
				t.Errorf("KeyDemoTenantID = %v, want %q", got, demoTenantID)
			}
			if got := ctx.Value(ctxkeys.KeyDemoUserID); got != demoUserID {
				t.Errorf("KeyDemoUserID = %v, want %q", got, demoUserID)
			}
			if got := ctx.Value(ctxkeys.KeyReadOnly); got != true {
				t.Errorf("KeyReadOnly = %v, want true", got)
			}

			// The security invariant: real credential slots stay empty so demo
			// traffic cannot bypass tenant-scoped rate limiting.
			if got := ctx.Value(ctxkeys.KeyTenantID); got != nil {
				t.Errorf("KeyTenantID = %v, want nil (demo must not set real tenant)", got)
			}
			if got := ctx.Value(ctxkeys.KeyUserID); got != nil {
				t.Errorf("KeyUserID = %v, want nil (demo must not set real user)", got)
			}
		})
	}
}

func TestDemoModePostAuth_NonDemoPassthrough(t *testing.T) {
	ctx := runDemoMiddleware(t, func(*http.Request) {
		// no demo triggers
	})

	if IsDemoMode(ctx) {
		t.Fatal("expected demo mode to be inactive")
	}
	for _, key := range []ctxkeys.Key{
		ctxkeys.KeyDemoMode,
		ctxkeys.KeyDemoTenantID,
		ctxkeys.KeyDemoUserID,
		ctxkeys.KeyReadOnly,
	} {
		if got := ctx.Value(key); got != nil {
			t.Errorf("non-demo request set %q = %v, want nil", key, got)
		}
	}
}

func TestDemoModePostAuth_HeaderMustBeExactlyTrue(t *testing.T) {
	// Guards against a loosened check: only the literal "true" arms demo mode.
	ctx := runDemoMiddleware(t, func(req *http.Request) {
		req.Header.Set("X-Demo-Mode", "1")
	})
	if IsDemoMode(ctx) {
		t.Fatal("X-Demo-Mode: 1 should not activate demo mode")
	}
}
