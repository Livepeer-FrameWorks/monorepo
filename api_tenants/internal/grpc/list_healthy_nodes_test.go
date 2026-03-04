package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

// nodeColumns matches the SELECT column order in scanNode.
var nodeColumns = []string{
	"id", "node_id", "cluster_id", "node_name", "node_type",
	"internal_ip", "external_ip", "wireguard_ip", "wireguard_public_key",
	"region", "availability_zone", "latitude", "longitude",
	"cpu_cores", "memory_gb", "disk_gb",
	"last_heartbeat", "created_at", "updated_at",
}

func newNodeRow(id, nodeID, clusterID, nodeName, nodeType, externalIP string) []driver.Value {
	now := time.Now()
	return []driver.Value{
		id, nodeID, clusterID, nodeName, nodeType,
		"10.0.0.1", // internal_ip
		externalIP, // external_ip
		nil,        // wireguard_ip
		nil,        // wireguard_public_key
		nil,        // region
		nil,        // availability_zone
		nil,        // latitude
		nil,        // longitude
		nil,        // cpu_cores
		nil,        // memory_gb
		nil,        // disk_gb
		nil,        // last_heartbeat
		now,        // created_at
		now,        // updated_at
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

	// Total count query (no tenant = platform official path)
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs(svcType).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Healthy count query
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(svcType, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Main query returning healthy nodes
	mock.ExpectQuery(`SELECT DISTINCT n\.id, n\.node_id`).
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

	// edge-egress is a capability registration (Foghorn → BootstrapService),
	// so it routes through listHealthyServiceNodes (service_instance join),
	// NOT the heartbeat path used by plain "edge".
	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\) FROM quartermaster\.infrastructure_nodes n`).
		WithArgs(edgeEgress).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`SELECT COUNT\(DISTINCT n\.id\)`).
		WithArgs(edgeEgress, int32(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectQuery(`SELECT DISTINCT n\.id, n\.node_id`).
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
