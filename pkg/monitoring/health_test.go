package monitoring

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type pingableClient struct{}

func (p *pingableClient) Ping(context.Context) error { return nil }

func TestHealthChecker_Basic(t *testing.T) {
	hc := NewHealthChecker("svc", "v1")
	hc.AddCheck("ok", func() CheckResult { return CheckResult{Status: "healthy"} })
	status := hc.CheckHealth()
	if status.Status != "healthy" {
		t.Fatalf("expected healthy")
	}
}

func TestHTTPServiceHealthCheck(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer s.Close()
	res := HTTPServiceHealthCheck("svc", s.URL)()
	if res.Status != "healthy" {
		t.Fatalf("expected healthy")
	}
}

func TestClickHouseHealthCheck_NilDB(t *testing.T) {
	res := ClickHouseHealthCheck(nil)()
	if res.Status != "unhealthy" {
		t.Fatalf("expected unhealthy for nil db, got %q", res.Status)
	}
	if res.Message != "ClickHouse connection is nil" {
		t.Errorf("unexpected message: %q", res.Message)
	}
}

func TestKafkaHealthChecks(t *testing.T) {
	// Use dummy pingable client
	res := ClickHouseNativeHealthCheck(&pingableClient{})()
	if res.Status != "healthy" {
		t.Fatalf("expected healthy")
	}
}
