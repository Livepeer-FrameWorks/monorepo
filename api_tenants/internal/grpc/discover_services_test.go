package grpc

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

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
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at",
	}

	// Unauthenticated path: filters by default cluster
	mock.ExpectQuery(`SELECT si\.id, si\.instance_id`).
		WithArgs("bridge", int32(51)). // limit = default 25 + 1
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-bridge-1", "bridge", "cluster-1", "node-1",
				"http", "10.0.0.1", int32(18000), nil, "running", "healthy", []byte(`{"wallet_address":"0xabc123"}`),
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
	if inst.GetHealthStatus() != "healthy" {
		t.Fatalf("expected health_status=healthy, got %s", inst.GetHealthStatus())
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
				"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
				"last_health_check", "created_at", "updated_at",
				"cluster_name", "base_url",
			}

			// This is a non-service caller (context.Background()), so the physical
			// ingress gate query does not run — public_instance_host is service-only.
			// Only pooled public_host synthesis is exercised here.
			// Pool path: query joins service_cluster_assignments + infrastructure_clusters,
			// so the args include the requested cluster_id and the SELECT pulls
			// cluster_name + base_url for public_host synthesis.
			mock.ExpectQuery(`(?s)JOIN quartermaster\.service_cluster_assignments sca.*JOIN quartermaster\.infrastructure_clusters c`).
				WithArgs("livepeer-gateway", tc.clusterID, int32(51)).
				WillReturnRows(sqlmock.NewRows(instanceCols).
					AddRow("uuid-1", "inst-lpgw-1", "livepeer-gateway", tc.clusterID, "core-eu-1",
						"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`),
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
// TestDiscoverServices_ServiceAuthNonDefaultClusterAndIngressGate pins two
// fixes at once: (1) a service-token caller is NOT restricted to the default
// cluster (the regression that silently disabled per-media-cluster gateway
// discovery), and (2) public_instance_host is only synthesized when a physical
// ingress site already covers that exact FQDN. The query matcher fails loudly
// if the default-cluster predicate ever reappears on the service-auth path.
func TestDiscoverServices_ServiceAuthNonDefaultClusterAndIngressGate(t *testing.T) {
	matcher := sqlmock.QueryMatcherFunc(func(expected, actual string) error {
		if strings.Contains(actual, "is_default_cluster") {
			return fmt.Errorf("service-auth discovery must not restrict to default cluster: %s", actual)
		}
		re, err := regexp.Compile(expected)
		if err != nil {
			return err
		}
		if !re.MatchString(actual) {
			return fmt.Errorf("query does not match %q: %s", expected, actual)
		}
		return nil
	})
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	const fqdn = "livepeer-gateway.core-eu-1.infra.frameworks.network"
	// Ingress gate: the physical site exists, so public_instance_host is allowed.
	mock.ExpectQuery(`(?s)ingress_sites si.*n\.status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"domains"}).AddRow([]byte(`["` + fqdn + `"]`)))

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url", "health_fresh",
	}
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments sca`).
		WithArgs("livepeer-gateway", "media-eu-1", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1",
				"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`),
				now, now, now, "Media EU 1", "frameworks.network", true))

	resp, err := server.DiscoverServices(serviceCtx(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1", // NOT the default cluster
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 1 {
		t.Fatalf("expected 1 instance for non-default cluster, got %d", len(resp.GetInstances()))
	}
	if got := resp.GetInstances()[0].GetMetadata()["public_instance_host"]; got != fqdn {
		t.Fatalf("public_instance_host = %q, want %q (ingress-gated, synthesized from node + platform root)", got, fqdn)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// When no physical ingress site exists for the instance, public_instance_host
// must NOT be advertised (don't hand out a non-routable name).
func TestDiscoverServices_IngressGateSuppressesUnprovisionedEndpoint(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	// No physical ingress sites provisioned yet.
	mock.ExpectQuery(`(?s)ingress_sites si.*n\.status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"domains"}))

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url", "health_fresh",
	}
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments sca`).
		WithArgs("livepeer-gateway", "media-eu-1", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1",
				"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`),
				now, now, now, "Media EU 1", "frameworks.network", true))

	resp, err := server.DiscoverServices(serviceCtx(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.GetInstances()[0].GetMetadata()["public_instance_host"]; got != "" {
		t.Fatalf("public_instance_host = %q, want empty (no physical ingress provisioned)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// Even with a provisioned physical ingress site, an instance Navigator would NOT
// publish a physical A record for (here: stale last_health_check) must not get a
// public_instance_host — discovery matches Navigator's publish predicate so Foghorn
// can't fan out to a non-routable hostname.
func TestDiscoverServices_IngressGateSuppressesIneligibleInstance(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	const fqdn = "livepeer-gateway.core-eu-1.infra.frameworks.network"
	// Physical ingress IS provisioned for this exact FQDN.
	mock.ExpectQuery(`(?s)ingress_sites si.*n\.status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"domains"}).AddRow([]byte(`["` + fqdn + `"]`)))

	now := time.Now()
	stale := now.Add(-10 * time.Minute) // realistic last_health_check; the health_fresh=false column (DB-evaluated) is what drives ineligibility
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url", "health_fresh",
	}
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments sca`).
		WithArgs("livepeer-gateway", "media-eu-1", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1",
				"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`),
				stale, now, now, "Media EU 1", "frameworks.network", false))

	resp, err := server.DiscoverServices(serviceCtx(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.GetInstances()[0].GetMetadata()["public_instance_host"]; got != "" {
		t.Fatalf("public_instance_host = %q, want empty (instance stale, not publish-eligible)", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// Malformed gate domains must fail the whole discovery (fail closed), not be
// silently skipped into a truncated provisioned set.
func TestDiscoverServices_FailsClosedOnMalformedGateDomains(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	mock.ExpectQuery(`(?s)ingress_sites si.*n\.status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"domains"}).AddRow([]byte(`{"not":"an array"}`)))

	_, err = server.DiscoverServices(serviceCtx(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on malformed gate domains, got %v", err)
	}
}

// A per-row scan/conversion error must fail closed, not skip the row into a
// truncated-but-"successful" gateway set Foghorn would cache.
func TestDiscoverServices_FailsClosedOnScanError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	mock.ExpectQuery(`(?s)ingress_sites si.*n\.status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"domains"}))

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url", "health_fresh",
	}
	// Non-numeric port → rows.Scan into int32 fails (column count matches, so the
	// failure is the port scan, not a width mismatch).
	mock.ExpectQuery(`JOIN quartermaster\.service_cluster_assignments sca`).
		WithArgs("livepeer-gateway", "media-eu-1", int32(51)).
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1",
				"http", "203.0.113.10", "NOT_AN_INT", nil, "running", "healthy", []byte(`{}`),
				now, now, now, "Media EU 1", "frameworks.network", true))

	_, err = server.DiscoverServices(serviceCtx(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on scan error, got %v", err)
	}
}

// A malformed domains row in ListIngressSites must fail closed, not nil the
// domains — otherwise Navigator's gate reads a provisioned site as "no matching
// domain" and could prune a valid infra A record.
func TestListIngressSites_FailsClosedOnMalformedDomains(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	s := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM quartermaster\.ingress_sites`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	now := time.Now()
	cols := []string{"id", "site_id", "cluster_id", "node_id", "domains", "tls_bundle_id", "kind", "upstream", "metadata", "created_at", "updated_at"}
	mock.ExpectQuery(`SELECT id, site_id, cluster_id, node_id, domains`).
		WillReturnRows(sqlmock.NewRows(cols).
			AddRow("id1", "site1", "cl1", "node1", []byte(`{"not":"an array"}`), "bundle1", "physical", "127.0.0.1:8935", []byte(`{}`), now, now))

	_, err = s.ListIngressSites(context.Background(), &pb.ListIngressSitesRequest{NodeId: "node1"})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on malformed ingress domains, got %v", err)
	}
}

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
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
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

// A JSONB domains column carrying a literal `null` (a non-array value) must fail
// closed: it unmarshals into a nil slice without error, which would read as "no
// domains" and silently suppress physical-endpoint synthesis or mislead a prune.
// Empty/absent (SQL NULL or []) is legitimately no domains and must not error.
func TestDecodeIngressDomainsStrict(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{"array decodes", `["a.example","b.example"]`, []string{"a.example", "b.example"}, false},
		{"empty array is no domains", `[]`, nil, false},
		{"empty bytes is no domains", ``, nil, false},
		{"json null fails closed", `null`, nil, true},
		{"whitespace-padded null fails closed", "  null\n", nil, true},
		{"object fails closed", `{"not":"an array"}`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeIngressDomainsStrict([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got domains %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("decode %q = %v, want %v", tc.raw, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("decode %q = %v, want %v", tc.raw, got, tc.want)
				}
			}
		})
	}
}

// A non-service caller (tenant/user/unauthenticated) must receive only the pooled
// public_host, never the per-node physical public_instance_host — the same
// service-only boundary ListServiceInstancesByType enforces. The physical ingress
// gate query must not run for them either: no expectation is set for it, so sqlmock
// fails the test if it executes.
func TestDiscoverServices_NonServiceCallerGetsNoPhysicalHost(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetPlatformRootDomain("frameworks.network")

	now := time.Now()
	instanceCols := []string{
		"id", "instance_id", "service_id", "cluster_id", "node_id",
		"protocol", "advertise_host", "port", "health_endpoint_override", "status", "health_status", "metadata",
		"last_health_check", "created_at", "updated_at", "cluster_name", "base_url",
	}
	// Only the instance query is expected. No ingress_sites gate query is set up;
	// if the code ran it for this non-service caller, sqlmock would fail on the
	// unexpected query.
	mock.ExpectQuery(`FROM quartermaster\.service_instances si`).
		WillReturnRows(sqlmock.NewRows(instanceCols).
			AddRow("uuid-1", "inst-1", "livepeer-gateway", "media-eu-1", "core-eu-1",
				"http", "203.0.113.10", int32(8935), nil, "running", "healthy", []byte(`{}`),
				now, now, now, "Media EU 1", "frameworks.network"))

	// context.Background() carries no auth_type and no tenant → not a service caller.
	// Pool-assigned discovery requires an explicit cluster_id.
	resp, err := server.DiscoverServices(context.Background(), &pb.ServiceDiscoveryRequest{
		ServiceType: "livepeer-gateway",
		ClusterId:   "media-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetInstances()) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(resp.GetInstances()))
	}
	md := resp.GetInstances()[0].GetMetadata()
	if got := md["public_instance_host"]; got != "" {
		t.Fatalf("public_instance_host = %q, want empty for a non-service caller", got)
	}
	if md["public_host"] == "" {
		t.Fatalf("expected pooled public_host to remain present for a non-service caller")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
