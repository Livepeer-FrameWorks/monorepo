package grpc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"frameworks/api_tenants/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
)

// A restarted Livepeer gateway is still marked unhealthy until the next health
// poll. Discovery probes it at once through the poller's check, persists the
// result, and returns it healthy in the same response.
func TestDiscoverServices_ProbesUnhealthyRunningGateway(t *testing.T) {
	var probes atomic.Int32
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		probes.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	host, portText, err := net.SplitHostPort(gateway.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	handlers.Init(db, logging.NewLogger())
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetInstanceHealthProber(handlers.ProbeDiscoveredInstances)

	now := time.Now()
	cols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url",
	}
	gatewayRow := func(health string) *sqlmock.Rows {
		return sqlmock.NewRows(cols).AddRow("uuid-1", "inst-lpgw-1", "livepeer-gateway", "media-eu", "core-eu-1",
			"http", host, int32(port), "/healthz", "running", health, []byte(`{}`),
			now.Add(-20*time.Second), now, now, "Media EU", "frameworks.network")
	}
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments`).WillReturnRows(gatewayRow("unhealthy"))
	mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).WithArgs("healthy", "inst-lpgw-1").
		WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow("unhealthy", "livepeer-gateway"))
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments`).WillReturnRows(gatewayRow("healthy"))

	resp, err := server.DiscoverServices(context.Background(), &quartermasterpb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway", ClusterId: "media-eu",
	})
	if err != nil {
		t.Fatalf("DiscoverServices: %v", err)
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("gateway /healthz probed %d times, want 1", got)
	}
	if len(resp.GetInstances()) != 1 || resp.GetInstances()[0].GetHealthStatus() != "healthy" {
		t.Fatalf("instances = %+v, want the gateway healthy", resp.GetInstances())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Healthy gateways, and other service types, are served from the stored
// health without a probe.
func TestDiscoverServices_DoesNotProbeHealthyGateways(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	var probed int
	server.SetInstanceHealthProber(func(context.Context, []quartermasterdb.ServiceDiscoveryRow) map[string]string {
		probed++
		return nil
	})

	now := time.Now()
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments`).WillReturnRows(sqlmock.NewRows([]string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url",
	}).AddRow("uuid-1", "inst-lpgw-1", "livepeer-gateway", "media-eu", "core-eu-1",
		"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`), now, now, now, "Media EU", "frameworks.network"))
	if _, err := server.DiscoverServices(context.Background(), &quartermasterpb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway", ClusterId: "media-eu",
	}); err != nil {
		t.Fatalf("DiscoverServices: %v", err)
	}
	if probed != 0 {
		t.Fatalf("healthy gateway probed %d times", probed)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
