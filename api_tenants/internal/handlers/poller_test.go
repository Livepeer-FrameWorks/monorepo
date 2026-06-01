package handlers

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
)

func TestServiceHealthSummarySnapshot(t *testing.T) {
	summary := newServiceHealthSummary()
	summary.recordResult("bridge", "healthy")
	summary.recordResult("bridge", "unhealthy")
	summary.recordResult("steward", "healthy")
	summary.recordSkipped("skipper")

	byService, healthyServices, unhealthyServices, skippedServices := summary.snapshot()

	if got := byService["bridge"]; got != (serviceHealthCounts{Checked: 2, Healthy: 1, Unhealthy: 1}) {
		t.Fatalf("bridge counts = %+v", got)
	}
	if got := byService["steward"]; got != (serviceHealthCounts{Checked: 1, Healthy: 1}) {
		t.Fatalf("steward counts = %+v", got)
	}
	if got := byService["skipper"]; got != (serviceHealthCounts{Skipped: 1}) {
		t.Fatalf("skipper counts = %+v", got)
	}
	if !reflect.DeepEqual(healthyServices, []string{"bridge", "steward"}) {
		t.Fatalf("healthy services = %v", healthyServices)
	}
	if !reflect.DeepEqual(unhealthyServices, []string{"bridge"}) {
		t.Fatalf("unhealthy services = %v", unhealthyServices)
	}
	if !reflect.DeepEqual(skippedServices, []string{"skipper"}) {
		t.Fatalf("skipped services = %v", skippedServices)
	}
}

func TestApplyServiceDefinitionFallbackUsesCanonicalHealthMetadata(t *testing.T) {
	inst := serviceInstance{serviceID: "vmauth"}

	applyServiceDefinitionFallback(&inst)

	if inst.defaultProto != "http" {
		t.Fatalf("defaultProto = %q, want http", inst.defaultProto)
	}
	if inst.path != "/health" {
		t.Fatalf("path = %q, want /health", inst.path)
	}
	if inst.port != 8427 {
		t.Fatalf("port = %d, want 8427", inst.port)
	}
}

func TestPollOnceRetriesSchemaVersionMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger())

	rows := sqlmock.NewRows([]string{
		"instance_id", "service_id", "cluster_id", "protocol", "advertise_host", "port",
		"path", "last_health_check", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	})
	mock.ExpectQuery("SELECT si.instance_id, si.service_id").
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 121, got 120"})
	mock.ExpectQuery("SELECT si.instance_id, si.service_id").
		WillReturnRows(rows)

	if err := pollOnce(&http.Client{Timeout: time.Millisecond}, make(chan struct{}, 1), 10, 0); err != nil {
		t.Fatalf("pollOnce returned error after retry: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPollOnceExcludesFoghornOwnedEdgeServices(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if expectedSQL != "poller excludes edge services" {
			return nil
		}
		if !strings.Contains(actualSQL, "s.type <> 'edge'") {
			return fmt.Errorf("poller query does not exclude aggregate edge service: %s", actualSQL)
		}
		if !strings.Contains(actualSQL, "s.type NOT LIKE 'edge-%'") {
			return fmt.Errorf("poller query does not exclude edge capability services: %s", actualSQL)
		}
		return nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()
	Init(mockDB, logging.NewLogger())

	rows := sqlmock.NewRows([]string{
		"instance_id", "service_id", "cluster_id", "protocol", "advertise_host", "port",
		"path", "last_health_check", "default_protocol", "assigned_cluster_id", "assigned_base_url",
	})
	mock.ExpectQuery("poller excludes edge services").WillReturnRows(rows)

	if err := pollOnce(&http.Client{Timeout: time.Millisecond}, make(chan struct{}, 1), 10, 0); err != nil {
		t.Fatalf("pollOnce returned error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyServiceDefinitionFallbackDoesNotOverrideInstanceProtocol(t *testing.T) {
	inst := serviceInstance{serviceID: "foghorn", proto: "grpc", port: 18029}

	applyServiceDefinitionFallback(&inst)

	if inst.proto != "grpc" {
		t.Fatalf("proto = %q, want grpc", inst.proto)
	}
	if inst.port != 18029 {
		t.Fatalf("port = %d, want 18029", inst.port)
	}
}

func TestGrpcHealthServerNameDefaultsToServiceInternalWithCA(t *testing.T) {
	if got := grpcHealthServerName(serviceInstance{serviceID: "decklog"}, "/etc/frameworks/pki/ca.crt", ""); got != "decklog.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestGrpcHealthServerNameHonorsExplicitValue(t *testing.T) {
	if got := grpcHealthServerName(serviceInstance{serviceID: "decklog"}, "/etc/frameworks/pki/ca.crt", "custom.internal"); got != "custom.internal" {
		t.Fatalf("server name = %q", got)
	}
}

func TestGrpcHealthTLSConfigUsesInternalNameForFoghornControlPort(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-eu-1",
		assignedBaseURL:   "https://frameworks.network",
		port:              18029,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}

func TestGrpcHealthTLSConfigHonorsFoghornExplicitServerName(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-us-1",
		assignedBaseURL:   "frameworks.network",
		port:              18029,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "foghorn.internal")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}

func TestGrpcHealthTLSConfigUsesInternalNameForFoghornInternalPort(t *testing.T) {
	inst := serviceInstance{
		serviceID:         "foghorn",
		assignedClusterID: "media-eu-1",
		assignedBaseURL:   "https://frameworks.network",
		port:              18019,
	}

	serverName, caFile := grpcHealthTLSConfig(inst, "/etc/frameworks/pki/ca.crt", "")
	if serverName != "foghorn.internal" {
		t.Fatalf("server name = %q", serverName)
	}
	if caFile != "/etc/frameworks/pki/ca.crt" {
		t.Fatalf("ca file = %q", caFile)
	}
}
