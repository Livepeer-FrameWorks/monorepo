package handlers

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func gatewayRow(t *testing.T, server *httptest.Server, instanceID string) quartermasterdb.ServiceDiscoveryRow {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return quartermasterdb.ServiceDiscoveryRow{
		InstanceID: instanceID, ServiceID: "livepeer-gateway", Protocol: "http",
		AdvertiseHost:  sql.NullString{String: host, Valid: true},
		Port:           sql.NullInt32{Int32: int32(port), Valid: true},
		HealthEndpoint: sql.NullString{String: "/healthz", Valid: true},
		Status:         "running", HealthStatus: "unhealthy",
	}
}

// Concurrent discoveries that find the same unhealthy gateway share one
// /healthz probe and one persisted result.
func TestProbeDiscoveredInstancesSharesOneProbePerInstance(t *testing.T) {
	var probes atomic.Int32
	release := make(chan struct{})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())
	mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).WithArgs("inst-gw-1", "healthy", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow("unhealthy", "livepeer-gateway"))

	row := gatewayRow(t, gateway, "inst-gw-1")
	const callers = 3
	var wg sync.WaitGroup
	results := make([]map[string]string, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = ProbeDiscoveredInstances(context.Background(), []quartermasterdb.ServiceDiscoveryRow{row})
		}()
	}
	// Let every caller join the in-flight probe before it answers.
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := probes.Load(); got != 1 {
		t.Fatalf("/healthz probed %d times for %d concurrent discoveries, want 1", got, callers)
	}
	for i, res := range results {
		if res["inst-gw-1"] != "healthy" {
			t.Fatalf("caller %d result = %v, want healthy", i, res)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A gateway that still fails its health check stays unhealthy, and that
// result is persisted like a poll's.
func TestProbeDiscoveredInstancesPersistsFailedCheck(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer gateway.Close()

	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())
	mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).WithArgs("inst-gw-2", "unhealthy", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow("unhealthy", "livepeer-gateway"))

	got := ProbeDiscoveredInstances(context.Background(), []quartermasterdb.ServiceDiscoveryRow{gatewayRow(t, gateway, "inst-gw-2")})
	if got["inst-gw-2"] != "unhealthy" {
		t.Fatalf("result = %v, want unhealthy", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
