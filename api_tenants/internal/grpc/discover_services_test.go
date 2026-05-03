package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDiscoverServices_MissingServiceType(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestDiscoverServices_PoolServiceRequiresClusterID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestDiscoverServices_ReturnsInstances(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "metadata",
		"last_health_check", "created_at", "updated_at",
	}

	// Unauthenticated path: filters by default cluster
	mock.ExpectQuery(`SELECT si\.id, si\.instance_id`).
		WithArgs("bridge", int32(51)). // limit = default 25 + 1
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-bridge-1", "bridge", "cluster-1", "node-1",
				"http", "10.0.0.1", int32(18000), nil, "running", []byte(`{"wallet_address":"0xabc123"}`),
				now, now, now))

	resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "bridge",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(resp.GetInstances()))
	}
	inst := resp.GetInstances()[0]
	if inst.GetInstanceId() != "inst-bridge-1" {
		t.Fatalf("expected instance_id=inst-bridge-1, got %s", inst.GetInstanceId())
	}
	if inst.GetServiceId() != "bridge" {
		t.Fatalf("expected service_id=bridge, got %s", inst.GetServiceId())
	}
	if inst.GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster_id=cluster-1, got %s", inst.GetClusterId())
	}
	if inst.GetPort() != 18000 {
		t.Fatalf("expected port=18000, got %d", inst.GetPort())
	}
	if inst.GetMetadata()["wallet_address"] != "0xabc123" {
		t.Fatalf("expected wallet metadata, got %v", inst.GetMetadata())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestDiscoverServices_PoolServiceMNPublicHostSynthesis pins the M:N
// invariant: a single physical livepeer-gateway instance assigned to two
// different media clusters returns a different public_host per request,
// derived from the requested cluster's DB metadata. public_host is never
// stored as static service_instances metadata for pool-assigned services.
func TestDiscoverServices_PoolServiceMNPublicHostSynthesis(t *testing.T) {
	cases := []struct {
		clusterID   string
		clusterName string
		baseURL     string
		wantPublic  string
	}{
		{"media-free-eu", "Media Free EU", "frameworks.network", "livepeer.media-free-eu.frameworks.network"},
		{"media-paid-eu", "Media Paid EU", "frameworks.network", "livepeer.media-paid-eu.frameworks.network"},
	}

	for _, tc := range cases {
		t.Run(tc.clusterID, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			now := time.Now()
			instanceCols := []string{
				"id", "instance_id", "service_id", "cluster_id", "node_id",
				"protocol", "advertise_host", "port", "health_endpoint_override", "status", "metadata",
				"last_health_check", "created_at", "updated_at",
				"cluster_name", "base_url",
			}

			// Pool path: query joins service_cluster_assignments + infrastructure_clusters,
			// so the args include the requested cluster_id and the SELECT pulls
			// cluster_name + base_url for public_host synthesis.
			mock.ExpectQuery(`(?s)JOIN quartermaster\.service_cluster_assignments sca.*JOIN quartermaster\.infrastructure_clusters c`).
				WithArgs("livepeer-gateway", tc.clusterID, int32(51)).
				WillReturnRows(sqlmock.NewRows(instanceCols).
					AddRow("uuid-1", "inst-lpgw-1", "livepeer-gateway", tc.clusterID, "core-eu-1",
						"http", "203.0.113.10", int32(8935), nil, "running", []byte(`{}`),
						now, now, now,
						tc.clusterName, tc.baseURL))

			resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
				ServiceType: "livepeer-gateway",
				ClusterId:   tc.clusterID,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetInstances()) != 1 {
				t.Fatalf("expected 1 instance, got %d", len(resp.GetInstances()))
			}
			inst := resp.GetInstances()[0]
			if got := inst.GetClusterId(); got != tc.clusterID {
				t.Fatalf("cluster_id = %q, want %q (logical assignment cluster, not physical)", got, tc.clusterID)
			}
			if got := inst.GetMetadata()["public_host"]; got != tc.wantPublic {
				t.Fatalf("public_host = %q, want %q (synthesized per requested cluster)", got, tc.wantPublic)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

// TestSynthesizePublicHostStripsSchemeAndPicksSlug isolates the FQDN derivation
// helper from the DB plumbing.
func TestSynthesizePublicHostStripsSchemeAndPicksSlug(t *testing.T) {
	cases := []struct {
		serviceType string
		clusterID   string
		clusterName string
		baseURL     string
		want        string
	}{
		{"livepeer-gateway", "media-central-primary", "Media Central Primary", "frameworks.network", "livepeer.media-central-primary.frameworks.network"},
		{"chandler", "media-central-primary", "Media Central Primary", "https://frameworks.network", "chandler.media-central-primary.frameworks.network"},
		{"foghorn", "media-central-primary", "Media Central Primary", "http://frameworks.network/", "foghorn.media-central-primary.frameworks.network"},
		// empty base URL → no FQDN
		{"livepeer-gateway", "media-central-primary", "Media", "", ""},
		// unknown service type → no FQDN
		{"not-a-service", "media-central-primary", "Media", "frameworks.network", ""},
	}
	for _, tc := range cases {
		got := synthesizePublicHost(tc.serviceType, tc.clusterID, tc.clusterName, tc.baseURL)
		if got != tc.want {
			t.Errorf("synthesizePublicHost(%q,%q,%q,%q) = %q, want %q", tc.serviceType, tc.clusterID, tc.clusterName, tc.baseURL, got, tc.want)
		}
	}
}

func TestDiscoverServices_EmptyResult(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "metadata",
		"last_health_check", "created_at", "updated_at",
	}

	mock.ExpectQuery(`SELECT si\.id, si\.instance_id`).
		WithArgs("nonexistent-service", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols))

	resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "nonexistent-service",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 0 {
		t.Fatalf("expected 0 instances, got %d", len(resp.GetInstances()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
