package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func listenTCP(t *testing.T, addr string) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("listen on %s: %v", addr, err)
	}
	return listener
}

// Re-registration resets an instance's stored check, and the instance is
// probed in the next round even though this replica probed it moments ago:
// a restarted Foghorn must not wait out the minimum probe age before
// Quartermaster names it as a peer address again. An instance whose stored
// check was not reset still waits for the minimum age.
func TestPollOnceProbesAReRegisteredInstanceDespiteMinAge(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())

	var mu sync.Mutex
	hits := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	host, portText, _ := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	port, _ := strconv.Atoi(portText)

	checkedAt := time.Now().Add(-time.Second)
	mock.ExpectQuery(`SELECT si\.instance_id, si\.service_id`).WillReturnRows(sqlmock.NewRows([]string{
		"instance_id", "service_id", "cluster_id", "protocol", "advertise_host", "port",
		"path", "last_health_check", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	}).
		AddRow("inst-reregistered", "bridge", "c1", "http", host, port, "/reregistered", nil, "http", "", "").
		AddRow("inst-steady", "bridge", "c1", "http", host, port, "/steady", checkedAt, "http", "", ""))
	mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).
		WithArgs("inst-reregistered", "healthy", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow("unknown", "bridge"))

	schedule := newProbeSchedule()
	schedule.probed["inst-reregistered"] = time.Now().Add(-time.Second)
	schedule.probed["inst-steady"] = time.Now().Add(-time.Second)
	if err := pollOnce(&http.Client{Timeout: time.Second}, make(chan struct{}, 2), 10, 30*time.Second, schedule, func(string) HealthWatchTLS { return HealthWatchTLS{} }); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if hits["/reregistered"] != 1 {
		t.Fatalf("re-registered instance probed %d times in the round after its registration; want 1", hits["/reregistered"])
	}
	if hits["/steady"] != 0 {
		t.Fatalf("instance probed %d times inside the minimum probe age; want 0", hits["/steady"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A gRPC health watch that ends because the instance stopped is opened again
// within seconds, so the restarted instance's SERVING is recorded as soon as
// it serves instead of at the next watch refresh.
func TestGrpcHealthWatchReopensSoonAfterTheInstanceRestarts(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())

	listener := listenTCP(t, "127.0.0.1:0")
	addr := listener.Addr().String()
	serve := func(lis net.Listener) (*grpc.Server, *health.Server) {
		srv := grpc.NewServer()
		hs := health.NewServer()
		healthpb.RegisterHealthServer(srv, hs)
		go func() { _ = srv.Serve(lis) }()
		return srv, hs
	}
	srv, hs := serve(listener)
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)

	persisted := func(oldStatus, newStatus string) {
		mock.ExpectQuery(`UPDATE quartermaster\.service_instances`).
			WithArgs("inst-fh", newStatus, sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"old_status", "service_id"}).AddRow(oldStatus, "bridge"))
	}
	awaitPersisted := func(within time.Duration, what string) {
		t.Helper()
		deadline := time.Now().Add(within)
		for {
			if mock.ExpectationsWereMet() == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s not recorded within %s: %v", what, within, mock.ExpectationsWereMet())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	mock.ExpectQuery(`instance_id`).WillReturnRows(sqlmock.NewRows([]string{
		"instance_id", "service_id", "advertise_host", "port", "protocol", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	}).AddRow("inst-fh", "bridge", host, port, "grpc", "grpc", "", ""))
	persisted("unknown", "healthy")
	m := &grpcWatchManager{
		active:  map[string]context.CancelFunc{},
		backoff: map[string]time.Time{},
		tls:     func(string) HealthWatchTLS { return HealthWatchTLS{AllowInsecure: true} },
	}
	sem := make(chan struct{}, 4)
	// The watch goroutine holds a semaphore slot until it returns; taking
	// every slot waits for it, so it cannot touch the next test's handles.
	defer func() {
		m.stopAll()
		timeout := time.After(5 * time.Second)
		for range cap(sem) {
			select {
			case sem <- struct{}{}:
			case <-timeout:
				t.Fatal("watch goroutine did not return after its watch was stopped")
			}
		}
	}()
	if err = m.refreshGrpcWatches(time.Second, time.Minute, sem); err != nil {
		t.Fatal(err)
	}
	awaitPersisted(5*time.Second, "SERVING of the running instance")

	// The instance drains its health watches the way a stopping service does,
	// then its process is gone.
	persisted("healthy", "unhealthy")
	hs.Shutdown()
	awaitPersisted(5*time.Second, "NOT_SERVING of the stopping instance")
	srv.Stop()

	persisted("unhealthy", "healthy")
	srv, _ = serve(listenTCP(t, addr))
	defer srv.Stop()
	awaitPersisted(5*time.Second, "SERVING of the restarted instance")
}

// An instance without the health Watch method is not redialed every few
// seconds: its watch gives up and waits out the configured backoff.
func TestGrpcHealthWatchWithoutWatchMethodBacksOff(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mockDB.Close() }()
	Init(mockDB, logging.NewLogger())

	listener := listenTCP(t, "127.0.0.1:0")
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(listener) }()
	defer srv.Stop()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)

	mock.ExpectQuery(`instance_id`).WillReturnRows(sqlmock.NewRows([]string{
		"instance_id", "service_id", "advertise_host", "port", "protocol", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	}).AddRow("inst-plain", "bridge", host, port, "grpc", "grpc", "", ""))
	m := &grpcWatchManager{
		active:  map[string]context.CancelFunc{},
		backoff: map[string]time.Time{},
		tls:     func(string) HealthWatchTLS { return HealthWatchTLS{AllowInsecure: true} },
	}
	sem := make(chan struct{}, 1)
	if err := m.refreshGrpcWatches(time.Second, time.Minute, sem); err != nil {
		t.Fatal(err)
	}
	select {
	case sem <- struct{}{}:
	case <-time.After(5 * time.Second):
		m.stopAll()
		t.Fatal("watch of an instance without the Watch method kept running")
	}
	m.mu.Lock()
	until, backedOff := m.backoff["inst-plain"]
	m.mu.Unlock()
	if !backedOff || time.Until(until) < 30*time.Second {
		t.Fatalf("instance without the Watch method not in backoff (until %v)", until)
	}
}
