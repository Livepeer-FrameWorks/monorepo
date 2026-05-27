package grpc

import (
	"context"
	"database/sql/driver"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

// nodeSnapshotTestColumns mirrors the snapshot columns the production
// SELECTs append after `..., updated_at`. Tests append these to nodeColumns
// / queryNodeColumns so the row width matches the production scanner.
var nodeSnapshotTestColumns = []string{
	"snapshot_cpu_percent", "snapshot_ram_used_bytes", "snapshot_ram_total_bytes",
	"snapshot_disk_used_bytes", "snapshot_disk_total_bytes", "snapshot_uptime_seconds", "snapshot_at",
}

// nodeColumns matches the SELECT column order in scanNode.
var nodeColumns = append([]string{
	"id", "node_id", "cluster_id", "node_name", "node_type",
	"internal_ip", "external_ip", "wireguard_ip", "wireguard_public_key", "wireguard_listen_port",
	"region", "availability_zone", "latitude", "longitude",
	"cpu_cores", "memory_gb", "disk_gb",
	"last_heartbeat", "enrollment_origin", "applied_mesh_revision", "status", "created_at", "updated_at",
	"owner_tenant_id",
}, nodeSnapshotTestColumns...)

// queryNodeColumns matches the SELECT column order in queryNode, which also
// returns the node's stored WireGuard listen port.
var queryNodeColumns = append([]string{
	"id", "node_id", "cluster_id", "node_name", "node_type",
	"internal_ip", "external_ip", "wireguard_ip", "wireguard_public_key", "wireguard_listen_port",
	"region", "availability_zone", "latitude", "longitude",
	"cpu_cores", "memory_gb", "disk_gb",
	"last_heartbeat", "enrollment_origin", "applied_mesh_revision", "status", "created_at", "updated_at",
	"owner_tenant_id",
}, nodeSnapshotTestColumns...)

func newNodeRow(id, nodeID, clusterID, nodeName, nodeType, externalIP string) []driver.Value {
	now := time.Now()
	return []driver.Value{
		id, nodeID, clusterID, nodeName, nodeType,
		"10.0.0.1",    // internal_ip
		externalIP,    // external_ip
		nil,           // wireguard_ip
		nil,           // wireguard_public_key
		nil,           // wireguard_listen_port
		nil,           // region
		nil,           // availability_zone
		nil,           // latitude
		nil,           // longitude
		nil,           // cpu_cores
		nil,           // memory_gb
		nil,           // disk_gb
		nil,           // last_heartbeat
		"gitops_seed", // enrollment_origin
		nil,           // applied_mesh_revision
		"active",      // status (matches the healthWhere SQL filter applied by tests)
		now,           // created_at
		now,           // updated_at
		"tenant-1",    // owner_tenant_id
		nil,           // snapshot_cpu_percent
		nil,           // snapshot_ram_used_bytes
		nil,           // snapshot_ram_total_bytes
		nil,           // snapshot_disk_used_bytes
		nil,           // snapshot_disk_total_bytes
		nil,           // snapshot_uptime_seconds
		nil,           // snapshot_at
	}
}

func TestListHealthyNodesForDNS_ServiceAuthUsesAllActiveClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	svcType := "bridge"
	serviceScope := `(?s)WHERE n\.cluster_id IN \(\s*SELECT c\.cluster_id FROM quartermaster\.infrastructure_clusters c\s*WHERE c\.is_active = true\s*\).*AND s\.type = \$1`

	mock.ExpectQuery(serviceScope).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(serviceScope).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(serviceScope).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "private-cluster", "node-1", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(ctx, &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
	if got := resp.GetNodes()[0].GetClusterId(); got != "private-cluster" {
		t.Fatalf("expected private-cluster, got %s", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_AnonymousUsesPlatformOfficialClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	svcType := "bridge"
	publicScope := `(?s)WHERE n\.cluster_id IN \(\s*SELECT c\.cluster_id FROM quartermaster\.infrastructure_clusters c\s*WHERE c\.public_topology = true AND c\.is_active = true\s*\).*AND s\.type = \$1`

	mock.ExpectQuery(publicScope).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(publicScope).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(publicScope).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "platform-cluster", "node-1", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
	if got := resp.GetNodes()[0].GetClusterId(); got != "platform-cluster" {
		t.Fatalf("expected platform-cluster, got %s", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_ServiceTypeReturnsMatchingNodes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	svcType := "bridge"

	// Total count query (no tenant = public topology path)
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Healthy count query
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Main query returning healthy nodes
	mock.ExpectQuery(`(?s)SELECT DISTINCT n\.id, n\.node_id, n\.cluster_id.*owner_tenant_id::text.*c\.cluster_id = n\.cluster_id`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "cluster-1", "node-1", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node, got %d", len(resp.GetNodes()))
	}
	if resp.GetNodes()[0].GetNodeId() != "node-1" {
		t.Fatalf("expected node-1, got %s", resp.GetNodes()[0].GetNodeId())
	}
	if resp.GetTotalNodes() != 1 {
		t.Fatalf("expected total_nodes=1, got %d", resp.GetTotalNodes())
	}
	if resp.GetHealthyNodes() != 1 {
		t.Fatalf("expected healthy_nodes=1, got %d", resp.GetHealthyNodes())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_TelemetryUsesVmauthInstances(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	publicType := "telemetry"
	lookupType := "vmauth"

	queryShape := `(?s)FROM quartermaster\.service_instances si.*JOIN quartermaster\.service_cluster_assignments sca ON sca\.service_instance_id = si\.id.*sca\.is_active = TRUE.*s\.type = \$1`
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT \(n\.id, sca\.cluster_id\)\) ` + queryShape).
		WithArgs(lookupType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT \(n\.id, sca\.cluster_id\)\) `+queryShape).
		WithArgs(lookupType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT DISTINCT n\.id, n\.node_id, sca\.cluster_id.*owner_tenant_id::text.*c\.cluster_id = sca\.cluster_id.*FROM quartermaster\.service_instances si.*JOIN quartermaster\.service_cluster_assignments sca`).
		WithArgs(lookupType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "regional-eu-1", "media-eu-1", "regional-eu-1", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &publicType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected telemetry to resolve backing vmauth node, got %d", len(resp.GetNodes()))
	}
	if got := resp.GetNodes()[0].GetClusterId(); got != "media-eu-1" {
		t.Fatalf("telemetry DNS cluster_id = %q, want assigned media cluster", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_ServiceTypeExcludesOtherServices(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	svcType := "bridge"

	// Total: 1 node matching bridge
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Healthy: 1 node
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Only bridge node returned (commodore-only node excluded by s.type filter)
	mock.ExpectQuery(`SELECT DISTINCT n\.id`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "cluster-1", "bridge-node", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 node (bridge only), got %d", len(resp.GetNodes()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_UnhealthyExcludedFromResultsButCountedInTotal(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	svcType := "bridge"

	// Total: 2 nodes have bridge instances (one healthy, one unhealthy)
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	// Healthy: only 1 is healthy
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Only the healthy node is returned
	mock.ExpectQuery(`SELECT DISTINCT n\.id`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "cluster-1", "healthy-bridge", "core", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotalNodes() != 2 {
		t.Fatalf("expected total_nodes=2, got %d", resp.GetTotalNodes())
	}
	if resp.GetHealthyNodes() != 1 {
		t.Fatalf("expected healthy_nodes=1, got %d", resp.GetHealthyNodes())
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 returned node, got %d", len(resp.GetNodes()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_CustomStaleThreshold(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	svcType := "bridge"

	// Total
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Healthy with custom threshold of 60s
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType, int32(60)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// No healthy nodes with strict threshold
	mock.ExpectQuery(`SELECT DISTINCT n\.id`).
		WithArgs(svcType, int32(60)).
		WillReturnRows(sqlmock.NewRows(nodeColumns))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType:           &svcType,
		StaleThresholdSeconds: 60,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotalNodes() != 1 {
		t.Fatalf("expected total_nodes=1, got %d", resp.GetTotalNodes())
	}
	if resp.GetHealthyNodes() != 0 {
		t.Fatalf("expected healthy_nodes=0 with strict threshold, got %d", resp.GetHealthyNodes())
	}
	if len(resp.GetNodes()) != 0 {
		t.Fatalf("expected 0 returned nodes, got %d", len(resp.GetNodes()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_EdgeQueryUsesHeartbeatNotServiceInstances(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	edgeSvc := "edge"

	// Edge path: total count — filters by node_type=edge
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs("edge").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	// Edge path: healthy count uses last_heartbeat (not si.health_status)
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs("edge", int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Edge path: main query returns healthy edge nodes
	mock.ExpectQuery(`SELECT DISTINCT n\.id, n\.node_id`).
		WithArgs("edge", int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "edge-1", "cluster-1", "edge-node-1", "edge", "1.2.3.4")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &edgeSvc,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotalNodes() != 2 {
		t.Fatalf("expected total_nodes=2, got %d", resp.GetTotalNodes())
	}
	if resp.GetHealthyNodes() != 1 {
		t.Fatalf("expected healthy_nodes=1, got %d", resp.GetHealthyNodes())
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 returned node, got %d", len(resp.GetNodes()))
	}
	if resp.GetNodes()[0].GetNodeType() != "edge" {
		t.Fatalf("expected node_type=edge, got %s", resp.GetNodes()[0].GetNodeType())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_EdgeSubtypeUsesServiceInstancePath(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	edgeEgress := "edge-egress"

	// edge-* subtypes are NOT pool-assigned services: an edge node's physical
	// cluster IS its logical media cluster, so service_instances.cluster_id is
	// authoritative. Routing therefore goes through the standard
	// listHealthyServiceNodes path (counts node ids only, no sca join).
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs(edgeEgress).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(edgeEgress, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`(?s)SELECT DISTINCT n\.id, n\.node_id, n\.cluster_id.*owner_tenant_id::text.*c\.cluster_id = n\.cluster_id`).
		WithArgs(edgeEgress, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "edge-1", "cluster-1", "edge-node-1", "edge", "5.6.7.8")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &edgeEgress,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 {
		t.Fatalf("expected 1 edge node with edge-egress capability, got %d", len(resp.GetNodes()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_FiltersByClusterID(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	serviceType := "edge-egress"
	clusterID := "cluster-1"

	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(clusterID, serviceType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(clusterID, serviceType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`(?s)SELECT DISTINCT n\.id, n\.node_id, n\.cluster_id.*n\.cluster_id = \$1.*s\.type = \$2`).
		WithArgs(clusterID, serviceType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "edge-1", clusterID, "edge-node-1", "edge", "5.6.7.8")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &serviceType,
		ClusterId:   &clusterID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetNodes()) != 1 || resp.GetNodes()[0].GetClusterId() != clusterID {
		t.Fatalf("expected one node in %s, got %#v", clusterID, resp.GetNodes())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestReportAliveNodesUpsertsEdgeCapabilities pins event-driven edge
// membership ingestion: for each NodeAliveness with an edge-* capability set, QM
// upserts the matching service_instances row. Caps not set are not
// materialised; existing rows for caps flipped off get UPDATE'd to unhealthy
// (covered by a separate test).
func TestReportAliveNodesUpsertsEdgeCapabilities(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// ensureServiceExists runs once per edge-* type (4 types), each in its own tx.
	for range []string{"edge-ingest", "edge-egress", "edge-storage", "edge-processing"} {
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).
			WithArgs(sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT service_id FROM quartermaster\.services WHERE service_id = \$1 OR name = \$1`).
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("edge-egress"))
		mock.ExpectCommit()
	}

	// Main tx: prior node read → update infrastructure_nodes → prior service_instances → per-cap upsert.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT node_id, cluster_id, COALESCE\(host\(external_ip\), ''\)\s+FROM quartermaster\.infrastructure_nodes\s+WHERE node_id = ANY\(\$1\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "cluster_id", "ext_ip"}).
			AddRow("edge-eu-1", "cluster-eu", "203.0.113.10"))
	mock.ExpectQuery(`(?s)SELECT si\.node_id, svc\.type, si\.cluster_id, COALESCE\(si\.health_status, ''\).*FROM quartermaster\.service_instances si.*svc\.type LIKE 'edge-%'`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "type", "cluster_id", "health"}))
	mock.ExpectExec(`(?s)UPDATE quartermaster\.infrastructure_nodes n.*FROM unnest`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Two caps on → two ON CONFLICT upserts. The other two caps are false →
	// no row exists → no UPDATE issued.
	for range []int{0, 1} {
		mock.ExpectExec(`(?s)INSERT INTO quartermaster\.service_instances.*SELECT \$1::varchar\(100\).*WHERE instance_id = \$1::varchar\(100\).*WHERE n\.node_id = \$2::varchar\(100\).*ON CONFLICT \(instance_id\) DO UPDATE.*updated_at = NOW\(\)\s*$`).
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	_, err = server.ReportAliveNodes(context.Background(), &pb.ReportAliveNodesRequest{
		Nodes: []*pb.NodeAliveness{{
			NodeId:     "edge-eu-1",
			IsHealthy:  true,
			ClusterId:  "cluster-eu",
			ExternalIp: "203.0.113.10",
			Capabilities: &pb.EdgeCapabilities{
				Ingest: true,
				Egress: true,
			},
		}},
	})
	if err != nil {
		t.Fatalf("ReportAliveNodes returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestReportAliveNodesMarksDroppedCapUnhealthy verifies that when a cap that
// previously had a healthy row goes false, we UPDATE the row to unhealthy.
// Don't delete; don't INSERT a fresh unhealthy row that was never advertised.
func TestReportAliveNodesMarksDroppedCapUnhealthy(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	for range []string{"edge-ingest", "edge-egress", "edge-storage", "edge-processing"} {
		mock.ExpectBegin()
		mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT service_id FROM quartermaster\.services`).WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("edge-egress"))
		mock.ExpectCommit()
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT node_id, cluster_id, COALESCE\(host\(external_ip\), ''\)\s+FROM quartermaster\.infrastructure_nodes`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "cluster_id", "ext_ip"}).
			AddRow("edge-eu-1", "cluster-eu", "203.0.113.10"))
	// Prior state: edge-egress instance was healthy. Now cap is off.
	mock.ExpectQuery(`(?s)SELECT si\.node_id, svc\.type, si\.cluster_id, COALESCE\(si\.health_status, ''\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "type", "cluster_id", "health"}).
			AddRow("edge-eu-1", "edge-egress", "cluster-eu", "healthy"))
	mock.ExpectExec(`(?s)UPDATE quartermaster\.infrastructure_nodes n.*FROM unnest`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Cap is off + existing row → UPDATE to unhealthy.
	mock.ExpectExec(`(?s)UPDATE quartermaster\.service_instances si\s+SET health_status = 'unhealthy'`).
		WithArgs("edge-egress", "edge-eu-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err = server.ReportAliveNodes(context.Background(), &pb.ReportAliveNodesRequest{
		Nodes: []*pb.NodeAliveness{{
			NodeId:       "edge-eu-1",
			IsHealthy:    true,
			ClusterId:    "cluster-eu",
			ExternalIp:   "203.0.113.10",
			Capabilities: &pb.EdgeCapabilities{}, // all caps off
		}},
	})
	if err != nil {
		t.Fatalf("ReportAliveNodes returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestListHealthyNodesForDNS_PoolServiceUsesAssignmentClusterForDNS pins the
// generic assignment-aware path: foghorn / chandler / livepeer-gateway all
// route through listHealthyAssignedServiceNodes. The returned cluster_id
// comes from sca.cluster_id (logical media cluster), not from si.cluster_id
// (physical/runtime cluster).
func TestListHealthyNodesForDNS_PoolServiceUsesAssignmentClusterForDNS(t *testing.T) {
	for _, svcType := range []string{"chandler", "foghorn", "livepeer-gateway"} {
		t.Run(svcType, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer func() { _ = db.Close() }()

			server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
			queryShape := `(?s)FROM quartermaster\.service_instances si.*JOIN quartermaster\.service_cluster_assignments sca ON sca\.service_instance_id = si\.id.*sca\.is_active = TRUE.*s\.type = \$1`

			mock.ExpectQuery(queryShape).
				WithArgs(svcType).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

			mock.ExpectQuery(queryShape).
				WithArgs(svcType, int32(300)).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

			mock.ExpectQuery(`(?s)SELECT DISTINCT n\.id, n\.node_id, sca\.cluster_id.*owner_tenant_id::text.*c\.cluster_id = sca\.cluster_id.*FROM quartermaster\.service_instances si.*JOIN quartermaster\.service_cluster_assignments sca`).
				WithArgs(svcType, int32(300)).
				WillReturnRows(sqlmock.NewRows(nodeColumns).
					AddRow(newNodeRow("uuid-1", "core-node-1", "media-central-primary", "core-node-1", "core", "1.2.3.4")...))

			svc := svcType
			resp, err := server.ListHealthyNodesForDNS(ctx, &pb.ListHealthyNodesForDNSRequest{
				ServiceType: &svc,
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetNodes()) != 1 {
				t.Fatalf("expected 1 assigned %s node, got %d", svcType, len(resp.GetNodes()))
			}
			if got := resp.GetNodes()[0].GetClusterId(); got != "media-central-primary" {
				t.Fatalf("expected assigned cluster media-central-primary, got %s", got)
			}

			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet sql expectations: %v", err)
			}
		})
	}
}

func TestListHealthyNodesForDNS_NoFilterReturnsAllHealthyNodes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// No service_type or node_type: returns all nodes with any healthy service instance
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	mock.ExpectQuery(`SELECT DISTINCT n\.id`).
		WithArgs(int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).
			AddRow(newNodeRow("uuid-1", "node-1", "cluster-1", "node-1", "core", "1.2.3.4")...).
			AddRow(newNodeRow("uuid-2", "node-2", "cluster-1", "node-2", "edge", "5.6.7.8")...))

	resp, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotalNodes() != 3 {
		t.Fatalf("expected total_nodes=3, got %d", resp.GetTotalNodes())
	}
	if resp.GetHealthyNodes() != 2 {
		t.Fatalf("expected healthy_nodes=2, got %d", resp.GetHealthyNodes())
	}
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("expected 2 returned nodes, got %d", len(resp.GetNodes()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListHealthyNodesForDNS_QueriesCastInetAddressesForAdvertiseHost(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(expectedSQL, actualSQL string) error {
		if strings.Contains(actualSQL, "external_ip <> ''") {
			return fmt.Errorf("query compares inet external_ip to empty string: %s", actualSQL)
		}
		if strings.Contains(actualSQL, "si.advertise_host = n.external_ip OR") || strings.Contains(actualSQL, "si.advertise_host = n.internal_ip)") {
			return fmt.Errorf("query compares advertise_host text directly to inet column: %s", actualSQL)
		}
		matched, matchErr := regexp.MatchString(expectedSQL, actualSQL)
		if matchErr != nil {
			return matchErr
		}
		if !matched {
			return fmt.Errorf("actual sql did not match %q: %s", expectedSQL, actualSQL)
		}
		return nil
	})))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	svcType := "bridge"

	mock.ExpectQuery(`(?s)si\.advertise_host = host\(n\.external_ip\).*si\.advertise_host = host\(n\.internal_ip\).*si\.advertise_host = host\(n\.wireguard_ip\)`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`(?s)si\.advertise_host = host\(n\.external_ip\).*si\.advertise_host = host\(n\.internal_ip\).*si\.advertise_host = host\(n\.wireguard_ip\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`(?s)si\.advertise_host = host\(n\.external_ip\).*si\.advertise_host = host\(n\.internal_ip\).*si\.advertise_host = host\(n\.wireguard_ip\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows(nodeColumns).AddRow(newNodeRow("uuid-1", "node-1", "cluster-1", "node-1", "core", "1.2.3.4")...))

	if _, err := server.ListHealthyNodesForDNS(context.Background(), &pb.ListHealthyNodesForDNSRequest{
		ServiceType: &svcType,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
