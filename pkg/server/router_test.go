package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
)

type debugTestConfig struct {
	Token string `env:"DEBUG_TEST_TOKEN" secret:"true" desc:"Secret token used by the debug test." introduced:"v0.3.0"`
	Mode  string `env:"DEBUG_TEST_MODE" default:"on" desc:"Plain value used by the debug test." introduced:"v0.3.0"`
}

func serve(t *testing.T, h http.Handler, method, path, remote string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func testRouterSpec(t *testing.T, name string) RouterSpec {
	t.Helper()
	return RouterSpec{
		Service: name,
		Logger:  logging.NewLogger(),
		Health:  monitoring.NewHealthChecker(name, "v1"),
		Metrics: monitoring.NewMetricsCollectorWithRegistry(name, "v1", "abc", prometheus.NewRegistry()),
	}
}

func TestNewServiceRouterServesReadinessSeparatelyFromLiveness(t *testing.T) {
	spec := testRouterSpec(t, "svc-ready")
	spec.Ready = monitoring.NewReadinessChecker("svc-ready", "v1")
	spec.Ready.AddCheck("store", func() monitoring.CheckResult {
		return monitoring.CheckResult{Status: monitoring.StatusUnhealthy, Message: "store unreachable"}
	})
	router := NewServiceRouter(spec)

	if w := serve(t, router, http.MethodGet, "/health", "127.0.0.1:1", nil); w.Code != http.StatusOK {
		t.Fatalf("/health = %d, want 200", w.Code)
	}
	if w := serve(t, router, http.MethodGet, "/ready", "127.0.0.1:1", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("/ready = %d, want 503 while a readiness dependency fails", w.Code)
	}
}

func TestDebugRoutesAreAbsentWithoutToken(t *testing.T) {
	router := NewServiceRouter(testRouterSpec(t, "svc-nodebug"))
	if w := serve(t, router, http.MethodGet, "/debug/pprof/cmdline", "127.0.0.1:1", nil); w.Code != http.StatusNotFound {
		t.Fatalf("/debug without token = %d, want 404", w.Code)
	}
}

func TestDebugRoutesRequireDirectPrivateAuthenticatedCaller(t *testing.T) {
	spec := testRouterSpec(t, "svc-debug")
	spec.DebugToken = "service-token"
	router := NewServiceRouter(spec)
	bearer := map[string]string{"Authorization": "Bearer service-token"}

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    int
	}{
		{name: "no token", remote: "127.0.0.1:1", want: http.StatusUnauthorized},
		{name: "wrong token", remote: "10.1.2.3:1", headers: map[string]string{"Authorization": "Bearer other"}, want: http.StatusUnauthorized},
		{name: "via reverse proxy", remote: "10.1.2.3:1", headers: map[string]string{"Authorization": "Bearer service-token", "X-Forwarded-For": "203.0.113.9"}, want: http.StatusForbidden},
		{name: "public peer", remote: "203.0.113.9:1", headers: bearer, want: http.StatusForbidden},
		{name: "private peer with token", remote: "10.1.2.3:1", headers: bearer, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := serve(t, router, http.MethodGet, "/debug/pprof/cmdline", tc.remote, tc.headers); w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestDebugConfigRedactsSecrets(t *testing.T) {
	spec := testRouterSpec(t, "svc-config")
	spec.DebugToken = "service-token"
	cfg := &debugTestConfig{Token: "hunter2", Mode: "on"}
	spec.DebugConfig = func() any { return cfg }
	spec.DebugConfigOptions = config.Options{Lookup: func(string) (string, bool) { return "", false }}
	router := NewServiceRouter(spec)

	w := serve(t, router, http.MethodGet, "/debug/config", "127.0.0.1:1", map[string]string{"Authorization": "Bearer service-token"})
	if w.Code != http.StatusOK {
		t.Fatalf("/debug/config = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "hunter2") {
		t.Fatalf("debug config leaked a secret: %s", body)
	}
	if !strings.Contains(body, config.RedactedValue) || !strings.Contains(body, "DEBUG_TEST_MODE") {
		t.Fatalf("debug config missing fields: %s", body)
	}
}

func TestIsPrivateClientIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1":        true,
		"10.0.0.8":         true,
		"172.18.0.1":       true,
		"fe80::1":          true,
		"::1":              true,
		"203.0.113.9":      false,
		"2001:db8::1":      false,
		"not-an-ip":        false,
		"::ffff:192.0.2.1": false,
	} {
		if got := IsPrivateClientIP(addr); got != want {
			t.Errorf("IsPrivateClientIP(%q) = %v, want %v", addr, got, want)
		}
	}
}
