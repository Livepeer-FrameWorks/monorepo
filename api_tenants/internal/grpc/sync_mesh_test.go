package grpc

import (
	"database/sql"
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSyncMeshUpdatesHeartbeatWithoutKeyOrPort(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, external_ip::text, internal_ip::text, cluster_id\s+FROM quartermaster\.infrastructure_nodes\s+WHERE node_id = \$1\s+AND status = 'active'`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "external_ip", "internal_ip", "cluster_id"}).AddRow("10.200.0.5", "1.2.3.4", "10.0.0.5", "cluster-1"))

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes\s+SET wireguard_public_key = COALESCE\(NULLIF\(\$1, ''\), wireguard_public_key\),\s+wireguard_listen_port = COALESCE\(NULLIF\(\$2, 0\), wireguard_listen_port\),\s+last_heartbeat = NOW\(\),\s+updated_at = NOW\(\)\s+WHERE node_id = \$3`).
		WithArgs("", int32(0), "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT n\.node_name, n\.wireguard_public_key, n\.external_ip::text, n\.internal_ip::text, n\.wireguard_ip::text, n\.wireguard_listen_port\s+FROM quartermaster\.infrastructure_nodes n\s+WHERE n\.node_id != \$1`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))

	mock.ExpectQuery(`SELECT s\.name, n\.wireguard_ip::text\s+FROM quartermaster\.services s\s+JOIN quartermaster\.service_instances si ON si\.service_id = s\.service_id\s+JOIN quartermaster\.infrastructure_nodes n ON n\.node_id = si\.node_id\s+WHERE si\.status IN \('running', 'active'\)\s+AND n\.wireguard_ip IS NOT NULL\s+AND n\.status = 'active'\s+AND n\.cluster_id = \$1`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "wireguard_ip"}))

	resp, err := server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{NodeId: "node-1"})
	if err != nil {
		t.Fatalf("sync mesh returned error: %v", err)
	}
	if resp.GetWireguardIp() != "10.200.0.5" {
		t.Fatalf("expected existing wireguard ip, got %q", resp.GetWireguardIp())
	}
	if resp.GetWireguardPort() != 51820 {
		t.Fatalf("expected default wireguard port, got %d", resp.GetWireguardPort())
	}
	if len(resp.GetPeers()) != 0 {
		t.Fatalf("expected no peers, got %d", len(resp.GetPeers()))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshRejectsInactiveNode(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, external_ip::text, internal_ip::text, cluster_id\s+FROM quartermaster\.infrastructure_nodes\s+WHERE node_id = \$1\s+AND status = 'active'`).
		WithArgs("inactive-node").
		WillReturnError(sql.ErrNoRows)

	_, err = server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{NodeId: "inactive-node"})
	if err == nil {
		t.Fatal("expected error for inactive node")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
