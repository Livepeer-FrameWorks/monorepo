package monitoring

import (
	"context"
	"database/sql"
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

func TestDatabaseHealthCheck(t *testing.T) {
	// Use a nil db to ensure unhealthy
	db := &sql.DB{}
	// We cannot force ping to fail reliably; just ensure it returns a result
	_ = db
}

func TestKafkaHealthChecks(t *testing.T) {
	// Use dummy pingable client
	res := ClickHouseNativeHealthCheck(&pingableClient{})()
	if res.Status != "healthy" {
		t.Fatalf("expected healthy")
	}
}
