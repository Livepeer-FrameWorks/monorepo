package grpc

import (
	"testing"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSyncMeshRequiresStoredWireGuardIdentity(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, wireguard_public_key, external_ip::text, internal_ip::text, wireguard_listen_port, cluster_id\s+FROM quartermaster\.infrastructure_nodes\s+WHERE node_id = \$1`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub-key-1", "1.2.3.4", "10.0.0.5", nil, "cluster-1"))

	_, err = server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub-key-1", ListenPort: 51820})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition when stored listen port is missing, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshRejectsPublicKeyMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, wireguard_public_key, external_ip::text, internal_ip::text, wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "stored-pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))

	_, err = server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "different-pub",
		ListenPort: 51820,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for public key mismatch, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshServiceEndpointsKeyedByType(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, wireguard_public_key, external_ip::text, internal_ip::text, wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub-key-1", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT n\.node_name, n\.wireguard_public_key`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))

	// Return services keyed by canonical service type
	mock.ExpectQuery(`SELECT s\.type, n\.wireguard_ip::text`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"type", "wireguard_ip"}).
			AddRow("bridge", "10.200.0.5").
			AddRow("commodore", "10.200.0.5"))

	resp, err := server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub-key-1",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh returned error: %v", err)
	}

	// Verify endpoints are keyed by canonical service type
	if _, ok := resp.GetServiceEndpoints()["bridge"]; !ok {
		t.Fatal("expected service_endpoints to contain key 'bridge'")
	}
	if _, ok := resp.GetServiceEndpoints()["commodore"]; !ok {
		t.Fatal("expected service_endpoints to contain key 'commodore'")
	}
	if _, ok := resp.GetServiceEndpoints()["Bridge"]; ok {
		t.Fatal("service_endpoints should NOT contain 'Bridge' (case mismatch)")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshReturnsStoredPortOverRequestEcho(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT wireguard_ip::text, wireguard_public_key, external_ip::text, internal_ip::text, wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51900), "cluster-1"))

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectQuery(`SELECT n\.node_name, n\.wireguard_public_key`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	mock.ExpectQuery(`SELECT s\.type, n\.wireguard_ip::text`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"type", "wireguard_ip"}))

	resp, err := server.SyncMesh(t.Context(), &pb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51900})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if resp.GetWireguardPort() != 51900 {
		t.Fatalf("WireguardPort = %d, want stored value 51900 (not request echo 51820)", resp.GetWireguardPort())
	}
	if resp.GetMeshRevision() == "" {
		t.Fatal("MeshRevision should be populated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestComputeMeshRevisionStableAndChanges(t *testing.T) {
	p1 := &pb.InfrastructurePeer{PublicKey: "a", Endpoint: "1.1.1.1:51820", AllowedIps: []string{"10.88.0.2/32"}, KeepAlive: 25}
	p2 := &pb.InfrastructurePeer{PublicKey: "b", Endpoint: "2.2.2.2:51820", AllowedIps: []string{"10.88.0.3/32"}, KeepAlive: 25}

	services := map[string]*pb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.1"}}}
	rev1 := computeMeshRevision([]*pb.InfrastructurePeer{p1, p2}, services, "10.88.0.1", 51820)
	rev2 := computeMeshRevision([]*pb.InfrastructurePeer{p2, p1}, map[string]*pb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.1"}}}, "10.88.0.1", 51820)
	if rev1 != rev2 {
		t.Fatalf("revision should be peer-order-independent: %s vs %s", rev1, rev2)
	}
	p3 := &pb.InfrastructurePeer{PublicKey: "c", Endpoint: "3.3.3.3:51820", AllowedIps: []string{"10.88.0.4/32"}, KeepAlive: 25}
	rev3 := computeMeshRevision([]*pb.InfrastructurePeer{p1, p2, p3}, services, "10.88.0.1", 51820)
	if rev1 == rev3 {
		t.Fatalf("revision should change when peer set changes: both %s", rev1)
	}
	revSelfChanged := computeMeshRevision([]*pb.InfrastructurePeer{p1, p2}, services, "10.88.0.99", 51820)
	if rev1 == revSelfChanged {
		t.Fatalf("revision should change when self IP changes: both %s", rev1)
	}
	revServiceChanged := computeMeshRevision([]*pb.InfrastructurePeer{p1, p2}, map[string]*pb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.9"}}}, "10.88.0.1", 51820)
	if rev1 == revServiceChanged {
		t.Fatalf("revision should change when service endpoints change: both %s", rev1)
	}
}
