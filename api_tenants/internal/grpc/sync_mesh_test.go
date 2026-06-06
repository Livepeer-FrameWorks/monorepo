package grpc

import (
	"bytes"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recentTimeArg struct {
	earliest time.Time
	latest   time.Time
}

func (a recentTimeArg) Match(v driver.Value) bool {
	t, ok := v.(time.Time)
	return ok && !t.Before(a.earliest) && !t.After(a.latest)
}

func expectMeshRequirements(mock sqlmock.Sqlmock, nodeID string, serviceTypes ...string) {
	rows := sqlmock.NewRows([]string{"type"})
	for _, serviceType := range serviceTypes {
		rows.AddRow(serviceType)
	}
	mock.ExpectQuery(`(?s)SELECT DISTINCT service_type\s+FROM \(`).
		WithArgs(nodeID).
		WillReturnRows(rows)
}

func expectMeshEndpoints(mock sqlmock.Sqlmock, clusterID, nodeID string, rows *sqlmock.Rows) {
	mock.ExpectQuery(`(?s)WITH request_contexts.*SELECT DISTINCT e\.type, e\.node_id, e\.wireguard_ip`).
		WithArgs(clusterID, nodeID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectInfraPeerIDs(mock sqlmock.Sqlmock, clusterID, nodeID string, nodeIDs ...string) {
	rows := sqlmock.NewRows([]string{"node_id"})
	for _, id := range nodeIDs {
		rows.AddRow(id)
	}
	mock.ExpectQuery(`(?s)WITH dependency_input AS .*SELECT DISTINCT e\.node_id`).
		WithArgs(clusterID, nodeID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectReciprocalProvidedServices(mock sqlmock.Sqlmock, nodeID string, serviceTypes ...string) {
	rows := sqlmock.NewRows([]string{"service_type"})
	for _, serviceType := range serviceTypes {
		rows.AddRow(serviceType)
	}
	mock.ExpectQuery(`(?s)SELECT DISTINCT service_type\s+FROM \(`).
		WithArgs(nodeID).
		WillReturnRows(rows)
}

func expectReciprocalDependentNodes(mock sqlmock.Sqlmock, clusterID, nodeID, targetType string, nodeIDs ...string) {
	rows := sqlmock.NewRows([]string{"node_id"})
	for _, id := range nodeIDs {
		rows.AddRow(id)
	}
	mock.ExpectQuery(`(?s)WITH dependency_input AS .*SELECT DISTINCT n\.node_id`).
		WithArgs(nodeID, clusterID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectMeshPeers(mock sqlmock.Sqlmock, nodeID, clusterID string, rows *sqlmock.Rows) {
	mock.ExpectQuery(`(?s)SELECT n\.node_name, n\.wireguard_public_key.*AND \(n\.cluster_id = \$2 OR n\.node_id = ANY\(\$3\)\)`).
		WithArgs(nodeID, clusterID, sqlmock.AnyArg()).
		WillReturnRows(rows)
}

func expectMeshConfigCacheMiss(mock sqlmock.Sqlmock, nodeID string) {
	mock.ExpectQuery(`(?s)SELECT c\.cluster_id,\s+c\.mesh_revision,\s+c\.topology_source_hash.*FROM quartermaster\.mesh_node_configs c\s+WHERE c\.node_id = \$1`).
		WithArgs(nodeID).
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id",
			"mesh_revision",
			"topology_source_hash",
			"wireguard_ip",
			"wireguard_port",
			"peers",
			"service_endpoints",
			"current_topology_source_hash",
		}))
}

func expectCurrentMeshTopologySourceHash(mock sqlmock.Sqlmock, revision int64) {
	mock.ExpectQuery(`SELECT COALESCE\(\(SELECT revision FROM quartermaster\.mesh_topology_state WHERE id = TRUE\), 0\)`).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(revision))
}

func expectMeshConfigStore(mock sqlmock.Sqlmock, nodeID, clusterID, wireguardIP string, wireguardPort int32, topologySourceHash string) {
	mock.ExpectExec(`(?s)INSERT INTO quartermaster\.mesh_node_configs`).
		WithArgs(nodeID, clusterID, sqlmock.AnyArg(), topologySourceHash, wireguardIP, wireguardPort, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func expectMeshWarmClaim(mock sqlmock.Sqlmock, revision int64) {
	mock.ExpectQuery(`(?s)UPDATE quartermaster\.mesh_topology_state\s+SET warming_started_at = NOW\(\).*RETURNING revision`).
		WithArgs(meshTopologyPlannerVersion).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(revision))
}

func expectMeshWarmNoClaim(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`(?s)UPDATE quartermaster\.mesh_topology_state\s+SET warming_started_at = NOW\(\).*RETURNING revision`).
		WithArgs(meshTopologyPlannerVersion).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}))
}

func expectMeshWarmFinish(mock sqlmock.Sqlmock, revision int64, success bool) {
	if success {
		mock.ExpectExec(`(?s)UPDATE quartermaster\.mesh_topology_state\s+SET warmed_revision = GREATEST\(warmed_revision, \$1\)`).
			WithArgs(revision, meshTopologyPlannerVersion).
			WillReturnResult(sqlmock.NewResult(0, 1))
		return
	}
	mock.ExpectExec(`(?s)UPDATE quartermaster\.mesh_topology_state\s+SET warming_started_at = NULL`).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestMeshServiceRequirementsMarksSkipperBridgeGlobal(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	expectMeshRequirements(mock, "central-1", "skipper")

	dnsRequired, peerRequired, globalPeerRequired, _, err := server.meshServiceRequirements(t.Context(), "central-1")
	if err != nil {
		t.Fatalf("meshServiceRequirements returned error: %v", err)
	}
	for name, set := range map[string]map[string]struct{}{
		"dns":    dnsRequired,
		"peer":   peerRequired,
		"global": globalPeerRequired,
	} {
		if _, ok := set["bridge"]; !ok {
			t.Fatalf("%s requirements missing bridge: %v", name, sortedStringKeys(set))
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMeshServiceRequirementsRetriesSchemaVersionMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	mock.ExpectQuery(`(?s)SELECT DISTINCT service_type\s+FROM \(`).
		WithArgs("central-1").
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 121, got 120"})
	expectMeshRequirements(mock, "central-1", "skipper")

	_, peerRequired, _, _, err := server.meshServiceRequirements(t.Context(), "central-1")
	if err != nil {
		t.Fatalf("meshServiceRequirements returned error after retry: %v", err)
	}
	if _, ok := peerRequired["bridge"]; !ok {
		t.Fatalf("peer requirements missing bridge after retry: %v", sortedStringKeys(peerRequired))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshRequiresStoredWireGuardIdentity(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id\s+FROM quartermaster\.infrastructure_nodes\s+WHERE node_id = \$1`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub-key-1", "1.2.3.4", "10.0.0.5", nil, "cluster-1"))

	_, err = server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub-key-1", ListenPort: 51820})
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

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "stored-pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))

	_, err = server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
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

func TestSyncMeshFailsClosedWhenTopologyQueryFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub-key-1", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	mock.ExpectQuery(`(?s)SELECT DISTINCT service_type\s+FROM \(`).
		WithArgs("node-1").
		WillReturnError(errors.New("db unavailable"))

	_, err = server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub-key-1",
		ListenPort: 51820,
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for topology query failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "mesh service requirements unavailable") {
		t.Fatalf("expected topology error context, got %v", err)
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

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub-key-1", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)

	expectMeshRequirements(mock, "node-1", "skipper")

	// Return services keyed by canonical service type.
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}).
		AddRow("bridge", "node-1", "10.200.0.5").
		AddRow("commodore", "node-1", "10.200.0.5"))
	expectInfraPeerIDs(mock, "cluster-1", "node-1")
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
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

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51900), "cluster-1"))

	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)

	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51900, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51900})
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

func TestSyncMeshCacheHitSkipsTopologyQueries(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT c\.cluster_id,\s+c\.mesh_revision,\s+c\.topology_source_hash.*FROM quartermaster\.mesh_node_configs c\s+WHERE c\.node_id = \$1`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id",
			"mesh_revision",
			"topology_source_hash",
			"wireguard_ip",
			"wireguard_port",
			"peers",
			"service_endpoints",
			"current_topology_source_hash",
		}).AddRow(
			"cluster-1",
			"cached-rev",
			meshTopologySourceHash(7),
			"10.200.0.5",
			int32(51820),
			[]byte(`[{"node_name":"peer-1","public_key":"peer-pub","endpoint":"203.0.113.10:51820","allowed_ips":["10.200.0.6/32"],"keep_alive":25}]`),
			[]byte(`{"quartermaster":{"ips":["10.200.0.1"]}}`),
			int64(7),
		))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51820})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if got := resp.GetMeshRevision(); got != "cached-rev" {
		t.Fatalf("MeshRevision = %q, want cached-rev", got)
	}
	if len(resp.GetPeers()) != 1 || resp.GetPeers()[0].GetNodeName() != "peer-1" {
		t.Fatalf("cached peers = %#v", resp.GetPeers())
	}
	if got := resp.GetServiceEndpoints()["quartermaster"].GetIps(); len(got) != 1 || got[0] != "10.200.0.1" {
		t.Fatalf("cached quartermaster endpoint = %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshStaleCacheServesStoredConfig(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT c\.cluster_id,\s+c\.mesh_revision,\s+c\.topology_source_hash.*FROM quartermaster\.mesh_node_configs c\s+WHERE c\.node_id = \$1`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id",
			"mesh_revision",
			"topology_source_hash",
			"wireguard_ip",
			"wireguard_port",
			"peers",
			"service_endpoints",
			"current_topology_source_hash",
		}).AddRow(
			"cluster-1",
			"cached-rev",
			meshTopologySourceHash(6),
			"10.200.0.5",
			int32(51820),
			[]byte(`[{"node_name":"peer-1","public_key":"peer-pub","endpoint":"203.0.113.10:51820","allowed_ips":["10.200.0.6/32"],"keep_alive":25}]`),
			[]byte(`{"quartermaster":{"ips":["10.200.0.1"]}}`),
			int64(7),
		))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51820})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if got := resp.GetMeshRevision(); got != "cached-rev" {
		t.Fatalf("MeshRevision = %q, want cached stale revision", got)
	}
	if len(resp.GetPeers()) != 1 || resp.GetPeers()[0].GetNodeName() != "peer-1" {
		t.Fatalf("cached peers = %#v", resp.GetPeers())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestClaimMeshTopologyWarmSkipsFreshRevision(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	expectMeshWarmNoClaim(mock)

	revision, claimed, err := server.claimMeshTopologyWarm(t.Context())
	if err != nil {
		t.Fatalf("claim mesh topology warm: %v", err)
	}
	if claimed {
		t.Fatalf("claimed fresh warm revision %d", revision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestWarmMeshTopologyConfigsRefreshesActiveNodes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expectMeshWarmClaim(mock, 9)
	mock.ExpectQuery(`(?s)SELECT node_id, cluster_id, host\(wireguard_ip\), wireguard_listen_port\s+FROM quartermaster\.infrastructure_nodes\s+WHERE status = 'active'`).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "cluster_id", "wireguard_ip", "wireguard_listen_port"}).
			AddRow("node-1", "cluster-1", "10.200.0.5", int32(51820)))
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(9))
	expectMeshWarmFinish(mock, 9, true)

	server.warmMeshTopologyConfigs(t.Context())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshReturnsComputedConfigWhenCacheWriteFails(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	mock.ExpectExec(`(?s)INSERT INTO quartermaster\.mesh_node_configs`).
		WithArgs("node-1", "cluster-1", sqlmock.AnyArg(), meshTopologySourceHash(1), "10.200.0.5", int32(51820), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(errors.New("cache write unavailable"))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51820})
	if err != nil {
		t.Fatalf("sync mesh should return computed config despite cache write failure: %v", err)
	}
	if resp.GetMeshRevision() == "" {
		t.Fatal("MeshRevision should be populated")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshReturnsCrossClusterPeersAndQuartermasterEndpoint(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("regional-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.88.1.20", "regional-pub", "203.0.113.20", "10.0.1.20", int32(51820), "regional"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("regional-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "regional-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "regional-1", "bridge")
	expectMeshEndpoints(mock, "regional", "regional-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}).
		AddRow("quartermaster", "central-1", "10.88.0.10").
		AddRow("purser", "central-1", "10.88.0.10"))
	expectReciprocalProvidedServices(mock, "regional-1")
	expectMeshPeers(mock, "regional-1", "regional", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}).
		AddRow("central-1", "central-pub", "203.0.113.10", nil, "10.88.0.10", int32(51820)))
	expectMeshConfigStore(mock, "regional-1", "regional", "10.88.1.20", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "regional-1",
		PublicKey:  "regional-pub",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if len(resp.GetPeers()) != 1 || resp.GetPeers()[0].GetNodeName() != "central-1" {
		t.Fatalf("expected cross-cluster central peer, got %#v", resp.GetPeers())
	}
	if got := resp.GetServiceEndpoints()["quartermaster"].GetIps(); len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("quartermaster endpoints = %v, want [10.88.0.10]", got)
	}
	if got := resp.GetServiceEndpoints()["purser"].GetIps(); len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("purser endpoints = %v, want [10.88.0.10]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshIncludesInfraDependencyPeersWithoutDNSAliases(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("regional-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.88.1.20", "regional-pub", "203.0.113.20", "10.0.1.20", int32(51820), "regional"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("regional-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "regional-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "regional-1", "purser")
	expectMeshEndpoints(mock, "regional", "regional-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}).
		AddRow("quartermaster", "qm-1", "10.88.0.10"))
	expectInfraPeerIDs(mock, "regional", "regional-1", "db-1", "kafka-1")
	expectReciprocalProvidedServices(mock, "regional-1")
	expectMeshPeers(mock, "regional-1", "regional", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}).
		AddRow("db-1", "db-pub", "203.0.113.20", nil, "10.88.0.20", int32(51820)).
		AddRow("kafka-1", "kafka-pub", "203.0.113.30", nil, "10.88.0.30", int32(51820)).
		AddRow("qm-1", "qm-pub", "203.0.113.10", nil, "10.88.0.10", int32(51820)))
	expectMeshConfigStore(mock, "regional-1", "regional", "10.88.1.20", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "regional-1",
		PublicKey:  "regional-pub",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if len(resp.GetPeers()) != 3 {
		t.Fatalf("peers = %d, want database+kafka+quartermaster", len(resp.GetPeers()))
	}
	if _, ok := resp.GetServiceEndpoints()["database"]; ok {
		t.Fatal("infra dependency should add peer reachability, not database.internal DNS")
	}
	if _, ok := resp.GetServiceEndpoints()["kafka"]; ok {
		t.Fatal("infra dependency should add peer reachability, not kafka.internal DNS")
	}
	if got := resp.GetServiceEndpoints()["quartermaster"].GetIps(); len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("quartermaster endpoints = %v, want [10.88.0.10]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestMeshServiceRequirementsIncludesPrivateerCertificateDependencies(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	expectMeshRequirements(mock, "yuga-1")

	dnsRequired, peerRequired, _, _, err := server.meshServiceRequirements(t.Context(), "yuga-1")
	if err != nil {
		t.Fatalf("meshServiceRequirements: %v", err)
	}
	for _, serviceID := range []string{"navigator", "quartermaster"} {
		if _, ok := dnsRequired[serviceID]; !ok {
			t.Fatalf("dnsRequired missing %s: %v", serviceID, sortedStringKeys(dnsRequired))
		}
		if _, ok := peerRequired[serviceID]; !ok {
			t.Fatalf("peerRequired missing %s: %v", serviceID, sortedStringKeys(peerRequired))
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshIncludesReciprocalServiceConsumers(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("central-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.88.0.10", "central-pub", "203.0.113.10", "10.0.0.10", int32(51820), "core"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("central-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "central-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "central-1", "quartermaster")
	expectMeshEndpoints(mock, "core", "central-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectInfraPeerIDs(mock, "core", "central-1")
	expectReciprocalProvidedServices(mock, "central-1", "quartermaster")
	expectReciprocalDependentNodes(mock, "core", "central-1", "quartermaster", "regional-1")
	expectMeshPeers(mock, "central-1", "core", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}).
		AddRow("regional-1", "regional-pub", "203.0.113.20", nil, "10.88.1.20", int32(51820)))
	expectMeshConfigStore(mock, "central-1", "core", "10.88.0.10", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "central-1",
		PublicKey:  "central-pub",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if len(resp.GetPeers()) != 1 || resp.GetPeers()[0].GetNodeName() != "regional-1" {
		t.Fatalf("expected reciprocal regional consumer peer, got %#v", resp.GetPeers())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCollectReciprocalServicePeerNodeIDsRetriesSchemaVersionMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	expectReciprocalProvidedServices(mock, "central-1", "quartermaster")
	mock.ExpectQuery(`(?s)WITH dependency_input AS .*SELECT DISTINCT n\.node_id`).
		WithArgs("central-1", "core", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 121, got 120"})
	expectReciprocalDependentNodes(mock, "core", "central-1", "quartermaster", "regional-1")

	peers, err := server.collectReciprocalServicePeerNodeIDs(t.Context(), "core", "central-1")
	if err != nil {
		t.Fatalf("collectReciprocalServicePeerNodeIDs returned error after retry: %v", err)
	}
	if _, ok := peers["regional-1"]; !ok {
		t.Fatalf("reciprocal peers missing regional-1 after retry: %v", sortedStringKeys(peers))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshMarksNodeActive(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`(?s)UPDATE quartermaster\.infrastructure_nodes.*status = 'active'`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	if _, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{NodeId: "node-1", PublicKey: "pub", ListenPort: 51820}); err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshIgnoresIncompleteResourceSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`(?s)UPDATE quartermaster\.infrastructure_nodes.*SET last_heartbeat = NOW\(\),.*updated_at = NOW\(\).*WHERE node_id = \$1`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	_, err = server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub",
		ListenPort: 51820,
		ResourceSnapshot: &quartermasterpb.NodeResourceSnapshot{
			CpuPercent:    17,
			RamTotalBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshStoresSnapshotAtReceiptTime(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).
			AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))

	before := time.Now().UTC()
	mock.ExpectExec(`(?s)UPDATE quartermaster\.infrastructure_nodes.*snapshot_cpu_percent = \$3.*snapshot_at = \$9`).
		WithArgs(
			"node-1",
			sqlmock.AnyArg(),
			float64(17.5),
			int64(512),
			int64(1024),
			int64(2048),
			int64(4096),
			int64(99),
			recentTimeArg{earliest: before, latest: before.Add(5 * time.Second)},
		).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	_, err = server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub",
		ListenPort: 51820,
		ResourceSnapshot: &quartermasterpb.NodeResourceSnapshot{
			CpuPercent:     17.5,
			RamUsedBytes:   512,
			RamTotalBytes:  1024,
			DiskUsedBytes:  2048,
			DiskTotalBytes: 4096,
			UptimeSeconds:  99,
			CollectedAt:    timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestComputeMeshRevisionStableAndChanges(t *testing.T) {
	p1 := &quartermasterpb.InfrastructurePeer{PublicKey: "a", Endpoint: "1.1.1.1:51820", AllowedIps: []string{"10.88.0.2/32"}, KeepAlive: 25}
	p2 := &quartermasterpb.InfrastructurePeer{PublicKey: "b", Endpoint: "2.2.2.2:51820", AllowedIps: []string{"10.88.0.3/32"}, KeepAlive: 25}

	services := map[string]*quartermasterpb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.1"}}}
	rev1 := computeMeshRevision([]*quartermasterpb.InfrastructurePeer{p1, p2}, services, "10.88.0.1", 51820)
	rev2 := computeMeshRevision([]*quartermasterpb.InfrastructurePeer{p2, p1}, map[string]*quartermasterpb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.1"}}}, "10.88.0.1", 51820)
	if rev1 != rev2 {
		t.Fatalf("revision should be peer-order-independent: %s vs %s", rev1, rev2)
	}
	p3 := &quartermasterpb.InfrastructurePeer{PublicKey: "c", Endpoint: "3.3.3.3:51820", AllowedIps: []string{"10.88.0.4/32"}, KeepAlive: 25}
	rev3 := computeMeshRevision([]*quartermasterpb.InfrastructurePeer{p1, p2, p3}, services, "10.88.0.1", 51820)
	if rev1 == rev3 {
		t.Fatalf("revision should change when peer set changes: both %s", rev1)
	}
	revSelfChanged := computeMeshRevision([]*quartermasterpb.InfrastructurePeer{p1, p2}, services, "10.88.0.99", 51820)
	if rev1 == revSelfChanged {
		t.Fatalf("revision should change when self IP changes: both %s", rev1)
	}
	revServiceChanged := computeMeshRevision([]*quartermasterpb.InfrastructurePeer{p1, p2}, map[string]*quartermasterpb.ServiceEndpoints{"quartermaster": {Ips: []string{"10.88.0.9"}}}, "10.88.0.1", 51820)
	if rev1 == revServiceChanged {
		t.Fatalf("revision should change when service endpoints change: both %s", rev1)
	}
}

// captureLogger returns a logrus logger that writes to an in-memory buffer.
// Tests inspect the buffer to assert that exclusion warnings were emitted.
func captureLogger() (*logrus.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := logrus.New()
	logger.SetOutput(buf)
	logger.SetLevel(logrus.WarnLevel)
	logger.SetFormatter(&logrus.JSONFormatter{})
	return logger, buf
}

func TestSyncMeshExcludesPeerWithMissingEndpoint(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	logger, logBuf := captureLogger()
	server := NewQuartermasterServer(db, logger, nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	// Peer row has both external_ip and internal_ip NULL — must be excluded
	// with a "missing_endpoint" warning, not silently skipped.
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}).
		AddRow("peer-orphan", "peer-pub", nil, nil, "10.200.0.6", int32(51820)))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if len(resp.GetPeers()) != 0 {
		t.Errorf("expected zero peers (orphan excluded), got %d", len(resp.GetPeers()))
	}

	out := logBuf.String()
	for _, want := range []string{
		`"reason":"missing_endpoint"`,
		`"node_name":"peer-orphan"`,
		`"requesting_node_id":"node-1"`,
		`"cluster_id":"cluster-1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exclusion warning missing %s\nlog: %s", want, out)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestSyncMeshExcludesPeerWithScanError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	logger, logBuf := captureLogger()
	server := NewQuartermasterServer(db, logger, nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT host\(wireguard_ip\), wireguard_public_key, host\(external_ip\), host\(internal_ip\), wireguard_listen_port, cluster_id`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_listen_port", "cluster_id"}).AddRow("10.200.0.5", "pub", "1.2.3.4", "10.0.0.5", int32(51820), "cluster-1"))
	mock.ExpectExec(`UPDATE quartermaster\.infrastructure_nodes`).
		WithArgs("node-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectMeshConfigCacheMiss(mock, "node-1")
	expectCurrentMeshTopologySourceHash(mock, 1)
	// Wrong column count triggers a Scan error — sqlmock reports too few
	// destinations vs source rows.
	expectMeshRequirements(mock, "node-1")
	expectMeshEndpoints(mock, "cluster-1", "node-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectReciprocalProvidedServices(mock, "node-1")
	expectMeshPeers(mock, "node-1", "cluster-1", sqlmock.NewRows([]string{"node_name"}).AddRow("peer-broken"))
	expectMeshConfigStore(mock, "node-1", "cluster-1", "10.200.0.5", 51820, meshTopologySourceHash(1))

	resp, err := server.SyncMesh(t.Context(), &quartermasterpb.InfrastructureSyncRequest{
		NodeId:     "node-1",
		PublicKey:  "pub",
		ListenPort: 51820,
	})
	if err != nil {
		t.Fatalf("sync mesh: %v", err)
	}
	if len(resp.GetPeers()) != 0 {
		t.Errorf("expected zero peers after scan error, got %d", len(resp.GetPeers()))
	}

	out := logBuf.String()
	for _, want := range []string{
		`"reason":"scan_error"`,
		`"requesting_node_id":"node-1"`,
		`"cluster_id":"cluster-1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan-error warning missing %s\nlog: %s", want, out)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
