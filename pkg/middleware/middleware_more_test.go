package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type capturingHook struct {
	infoMsgs []string
}

func (h *capturingHook) Levels() []logrus.Level { return logrus.AllLevels }
func (h *capturingHook) Fire(e *logrus.Entry) error {
	if e.Level == logrus.InfoLevel {
		h.infoMsgs = append(h.infoMsgs, e.Message)
	}
	return nil
}

func newCapturingLogger() (logging.Logger, *capturingHook) {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	hook := &capturingHook{}
	l.AddHook(hook)
	return l, hook
}

func TestShortMethodName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/commodore.CommodoreService/Login", "Login"},
		{"Login", "Login"},
		{"", ""},
		{"/", "/"},
		{"svc/", "svc/"},
		{"/Login", "Login"},
		{"a/b/c", "c"},
	}
	for _, tc := range cases {
		if got := shortMethodName(tc.in); got != tc.want {
			t.Fatalf("shortMethodName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseMetadataPolicy(t *testing.T) {
	cases := []struct {
		in   string
		want ServiceTokenMetadataPolicy
	}{
		{"audit", MetadataPolicyAudit},
		{"  Audit ", MetadataPolicyAudit},
		{"allow", MetadataPolicyAllow},
		{"", MetadataPolicyAllow},
		{"garbage", MetadataPolicyAllow},
	}
	for _, tc := range cases {
		if got := parseMetadataPolicy(tc.in); got != tc.want {
			t.Fatalf("parseMetadataPolicy(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestExtractMetadataToContext(t *testing.T) {
	t.Run("only user id injected", func(t *testing.T) {
		md := metadata.New(map[string]string{"x-user-id": "u1"})
		ctx := extractMetadataToContext(context.Background(), md, MetadataPolicyAllow, nil, "/m")
		if got := ctxkeys.GetUserID(ctx); got != "u1" {
			t.Fatalf("user id = %q, want u1", got)
		}
		if got := ctxkeys.GetTenantID(ctx); got != "" {
			t.Fatalf("tenant id = %q, want empty", got)
		}
	})

	t.Run("only tenant id injected", func(t *testing.T) {
		md := metadata.New(map[string]string{"x-tenant-id": "t1"})
		ctx := extractMetadataToContext(context.Background(), md, MetadataPolicyAllow, nil, "/m")
		if got := ctxkeys.GetTenantID(ctx); got != "t1" {
			t.Fatalf("tenant id = %q, want t1", got)
		}
		if got := ctxkeys.GetUserID(ctx); got != "" {
			t.Fatalf("user id = %q, want empty", got)
		}
	})

	t.Run("empty metadata injects nothing", func(t *testing.T) {
		md := metadata.New(map[string]string{})
		ctx := extractMetadataToContext(context.Background(), md, MetadataPolicyAllow, nil, "/m")
		if got := ctxkeys.GetUserID(ctx); got != "" {
			t.Fatalf("user id = %q, want empty", got)
		}
		if got := ctxkeys.GetTenantID(ctx); got != "" {
			t.Fatalf("tenant id = %q, want empty", got)
		}
	})
}

func auditCount(msgs []string) int {
	n := 0
	for _, m := range msgs {
		if m == "Service token metadata injection (audit)" {
			n++
		}
	}
	return n
}

func TestExtractMetadataToContextAuditLogging(t *testing.T) {
	t.Run("audit logs when metadata present", func(t *testing.T) {
		log, hook := newCapturingLogger()
		md := metadata.New(map[string]string{"x-user-id": "u1"})
		extractMetadataToContext(context.Background(), md, MetadataPolicyAudit, log, "/m")
		if got := auditCount(hook.infoMsgs); got != 1 {
			t.Fatalf("expected 1 audit info call, got %d", got)
		}
	})

	t.Run("audit silent when no metadata", func(t *testing.T) {
		log, hook := newCapturingLogger()
		md := metadata.New(map[string]string{})
		extractMetadataToContext(context.Background(), md, MetadataPolicyAudit, log, "/m")
		if got := auditCount(hook.infoMsgs); got != 0 {
			t.Fatalf("expected no audit info call, got %d", got)
		}
	})

	t.Run("allow policy never audit-logs", func(t *testing.T) {
		log, hook := newCapturingLogger()
		md := metadata.New(map[string]string{"x-user-id": "u1", "x-tenant-id": "t1"})
		extractMetadataToContext(context.Background(), md, MetadataPolicyAllow, log, "/m")
		if got := auditCount(hook.infoMsgs); got != 0 {
			t.Fatalf("expected no audit info call for allow policy, got %d", got)
		}
	})

	t.Run("audit silent when logger nil", func(t *testing.T) {
		md := metadata.New(map[string]string{"x-user-id": "u1"})
		extractMetadataToContext(context.Background(), md, MetadataPolicyAudit, nil, "/m")
	})
}

func TestGRPCAuthInterceptorExplicitPolicyAuditsMetadata(t *testing.T) {
	log, hook := newCapturingLogger()
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		ServiceToken:   "svc",
		MetadataPolicy: MetadataPolicyAudit,
		Logger:         log,
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer svc",
		"x-tenant-id":   "tenant-x",
	}))
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}
	handler := func(ctx context.Context, req any) (any, error) {
		if got := ctxkeys.GetTenantID(ctx); got != "tenant-x" {
			t.Fatalf("tenant id = %q, want tenant-x", got)
		}
		return struct{}{}, nil
	}
	if _, err := interceptor(ctx, struct{}{}, info, handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := auditCount(hook.infoMsgs); got != 1 {
		t.Fatalf("expected 1 audit info call from interceptor, got %d", got)
	}
}

func TestGRPCAuthInterceptorRejections(t *testing.T) {
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{ServiceToken: "svc", JWTSecret: []byte("k")})
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}
	handler := func(ctx context.Context, req any) (any, error) { return struct{}{}, nil }

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"missing metadata", context.Background()},
		{"missing authorization", metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{}))},
		{"bad prefix", metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{"authorization": "Basic abc"}))},
		{"invalid token", metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{"authorization": "Bearer wrong"}))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := interceptor(tc.ctx, struct{}{}, info, handler)
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("expected Unauthenticated, got %v", err)
			}
		})
	}
}

func TestGRPCAuthInterceptorEmptyJWTSecretSkipsJWTValidation(t *testing.T) {
	// A token forged with an empty HMAC key must NOT authenticate when the
	// server has no JWT secret configured; the JWT branch is gated on a
	// non-empty secret.
	forged, err := auth.GenerateJWT("u", "t", "e@x", "member", []byte{})
	if err != nil {
		t.Fatalf("generate forged token: %v", err)
	}
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{ServiceToken: "svc", JWTSecret: nil})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + forged,
	}))
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.S/M"}
	handler := func(context.Context, any) (any, error) {
		t.Fatal("handler must not run for empty-secret config")
		return nil, nil
	}
	_, err = interceptor(ctx, struct{}{}, info, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func newCORSRecorder(t *testing.T, allowed []string, method, path, origin string, reqMethod, reqHeaders string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORSMiddleware(allowed, false))
	r.Any("/*any", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if reqMethod != "" {
		req.Header.Set("Access-Control-Request-Method", reqMethod)
	}
	if reqHeaders != "" {
		req.Header.Set("Access-Control-Request-Headers", reqHeaders)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCORSAllowedOriginEchoesRequestMethodAndHeaders(t *testing.T) {
	origin := "https://app.frameworks.network"
	allowed := []string{origin}

	t.Run("echoes provided request method and headers", func(t *testing.T) {
		w := newCORSRecorder(t, allowed, http.MethodOptions, "/api", origin, "PATCH", "x-custom")
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != "PATCH" {
			t.Fatalf("expected echoed method PATCH, got %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); got != "x-custom" {
			t.Fatalf("expected echoed headers x-custom, got %q", got)
		}
	})

	t.Run("falls back to defaults when absent", func(t *testing.T) {
		w := newCORSRecorder(t, allowed, http.MethodOptions, "/api", origin, "", "")
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, OPTIONS" {
			t.Fatalf("expected default methods, got %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); got != publicCORSAllowHeaders {
			t.Fatalf("expected default headers, got %q", got)
		}
	})
}

func TestCORSPublicOriginEchoesRequestMethodAndHeaders(t *testing.T) {
	origin := "https://third-party.example"
	allowed := []string{"https://app.frameworks.network"}

	t.Run("echoes provided request method and headers", func(t *testing.T) {
		w := newCORSRecorder(t, allowed, http.MethodOptions, "/graphql", origin, "PATCH", "x-custom")
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("expected wildcard origin, got %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != "PATCH" {
			t.Fatalf("expected echoed method PATCH, got %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); got != "x-custom" {
			t.Fatalf("expected echoed headers x-custom, got %q", got)
		}
	})

	t.Run("falls back to public defaults when absent", func(t *testing.T) {
		w := newCORSRecorder(t, allowed, http.MethodOptions, "/graphql", origin, "", "")
		if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, OPTIONS" {
			t.Fatalf("expected default public methods, got %q", got)
		}
		if got := w.Header().Get("Access-Control-Allow-Headers"); got != publicCORSAllowHeaders {
			t.Fatalf("expected default headers, got %q", got)
		}
	})
}
