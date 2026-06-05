package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBootstrapInfrastructureNode_MissingToken(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		NodeType: "core",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestBootstrapInfrastructureNode_MissingNodeType(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token: "my-token",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestBootstrapInfrastructureNode_InvalidToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("bad-token")).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:    "bad-token",
		NodeType: "core",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapInfrastructureNode_ExpiredToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiredAt := time.Now().Add(-1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("expired-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip", "metadata"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiredAt, nil, "{}"))
	mock.ExpectRollback()

	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:    "expired-token",
		NodeType: "core",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapInfrastructureNode_ClusterMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("bound-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip", "metadata"}).
			AddRow("tok-1", "tenant-1", "cluster-a", nil, int32(0), expiresAt, nil, "{}"))
	mock.ExpectRollback()

	targetCluster := "cluster-b"
	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:           "bound-token",
		NodeType:        "core",
		TargetClusterId: &targetCluster,
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapInfrastructureNode_IdempotentReturnsExisting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("my-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip", "metadata"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil, "{}"))

	// Idempotent check: node already exists in same cluster.
	// New behaviour: return full assigned identity so a retrying client can
	// recover without any server-side cleanup.
	mock.ExpectQuery(`SELECT cluster_id, host\(wireguard_ip\), wireguard_listen_port FROM quartermaster.infrastructure_nodes`).
		WithArgs("node-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_ip", "wireguard_listen_port"}).
			AddRow("cluster-1", "10.88.0.2", int32(51820)))
	mock.ExpectCommit()
	// Cluster mesh config + seed peer/service lookups after the commit.
	mock.ExpectQuery(`SELECT wg_mesh_cidr, wg_listen_port FROM quartermaster.infrastructure_clusters`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"wg_mesh_cidr", "wg_listen_port"}).AddRow("10.88.0.0/16", int32(51820)))
	expectMeshRequirements(mock, "node-existing")
	expectMeshEndpoints(mock, "cluster-1", "node-existing", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectMeshPeers(mock, "node-existing", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))

	resp, err := server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:    "my-token",
		NodeType: "core",
		NodeId:   strPtr("node-existing"),
	})
	if err != nil {
		t.Fatalf("expected success for idempotent call, got: %v", err)
	}
	if resp.GetNodeId() != "node-existing" {
		t.Fatalf("expected node_id=node-existing, got %s", resp.GetNodeId())
	}
	if resp.GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster_id=cluster-1, got %s", resp.GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapInfrastructureNode_ExistingNodeDifferentCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("my-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip", "metadata"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil, "{}"))

	// Node exists but in a different cluster
	mock.ExpectQuery(`SELECT cluster_id, host\(wireguard_ip\), wireguard_listen_port FROM quartermaster.infrastructure_nodes`).
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_ip", "wireguard_listen_port"}).
			AddRow("cluster-other", nil, nil))
	mock.ExpectRollback()

	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:    "my-token",
		NodeType: "core",
		NodeId:   strPtr("node-1"),
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapInfrastructureNode_FallbackCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(cluster_id, ''\), expires_at, expected_ip::text`).
		WithArgs(hashBootstrapToken("my-token")).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "expires_at", "expected_ip"}).
			AddRow("", expiresAt, nil))
	mock.ExpectQuery(`SELECT\s+n\.cluster_id,\s+n\.wireguard_public_key`).
		WithArgs("node-fallback").
		WillReturnError(sql.ErrNoRows)

	// Token has no cluster binding
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("my-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip", "metadata"}).
			AddRow("tok-1", "tenant-1", "", nil, int32(0), expiresAt, nil, "{}"))

	// No target_cluster_id in request, so falls back to first active cluster
	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("fallback-cluster"))

	// Node doesn't exist yet
	mock.ExpectQuery(`SELECT cluster_id, host\(wireguard_ip\), wireguard_listen_port FROM quartermaster.infrastructure_nodes`).
		WithArgs("node-fallback").
		WillReturnError(sql.ErrNoRows)

	// Cluster mesh config lookup.
	mock.ExpectQuery(`SELECT wg_mesh_cidr, wg_listen_port`).
		WithArgs("fallback-cluster").
		WillReturnRows(sqlmock.NewRows([]string{"wg_mesh_cidr", "wg_listen_port"}).AddRow("10.88.0.0/16", int32(51820)))

	// Taken-IPs lookup for allocator.
	mock.ExpectQuery(`SELECT host\(wireguard_ip\)`).
		WithArgs("fallback-cluster").
		WillReturnRows(sqlmock.NewRows([]string{"wireguard_ip"}))

	// INSERT node: id, node_id, cluster_id, node_name, node_type,
	// external_ip, internal_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port,
	// latitude, longitude, metadata. enrollment_origin is hard-coded 'runtime_enrolled' in the INSERT.
	mock.ExpectExec("INSERT INTO quartermaster.infrastructure_nodes").
		WithArgs(sqlmock.AnyArg(), "node-fallback", "fallback-cluster", "node-fallback", "core", nil, nil, sqlmock.AnyArg(), "pub-key", int32(51820), nil, nil, "{}").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Token usage update
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs("tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectCommit()
	expectMeshRequirements(mock, "node-fallback")
	expectMeshEndpoints(mock, "fallback-cluster", "node-fallback", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectMeshPeers(mock, "node-fallback", "fallback-cluster", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))

	wgPub := "pub-key"
	nodeID := "node-fallback"
	resp, err := server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:              "my-token",
		NodeType:           "core",
		NodeId:             &nodeID,
		WireguardPublicKey: &wgPub,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetClusterId() != "fallback-cluster" {
		t.Fatalf("expected fallback-cluster, got %s", resp.GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestBootstrapInfrastructureNode_ReplayWithSpentToken exercises the
// replay branch: caller supplies node_id + public_key that match an
// existing row, together with the original token (now spent). The server
// returns the full existing assignment without touching usage counters.
func TestBootstrapInfrastructureNode_ReplayWithSpentToken(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetQuartermasterGRPCAddr("qm.internal:19002")

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()

	// Replay token lookup: finds the token row regardless of usage.
	mock.ExpectQuery(`SELECT COALESCE\(cluster_id, ''\), expires_at, expected_ip::text`).
		WithArgs(hashBootstrapToken("spent-token")).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "expires_at", "expected_ip"}).
			AddRow("cluster-1", expiresAt, nil))

	// Existing node lookup with matching public key.
	mock.ExpectQuery(`SELECT\s+n\.cluster_id,\s+n\.wireguard_public_key`).
		WithArgs("node-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_public_key", "wireguard_ip", "wireguard_listen_port", "owner_tenant_id"}).
			AddRow("cluster-1", "pub-matches", "10.88.0.5", int32(51820), nil))

	// After replay matches, the helper uses s.db (not tx) for cluster cfg
	// + seed peer/service lookups; sqlmock treats them the same.
	mock.ExpectQuery(`SELECT wg_mesh_cidr, wg_listen_port FROM quartermaster.infrastructure_clusters`).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"wg_mesh_cidr", "wg_listen_port"}).AddRow("10.88.0.0/16", int32(51820)))
	expectMeshRequirements(mock, "node-existing")
	expectMeshEndpoints(mock, "cluster-1", "node-existing", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}))
	expectMeshPeers(mock, "node-existing", "cluster-1", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}))

	// No token-usage UPDATE, no INSERT, no Commit — replay returns without
	// writing. The deferred tx.Rollback handles cleanup.
	mock.ExpectRollback()

	wgPub := "pub-matches"
	resp, err := server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:              "spent-token",
		NodeType:           "core",
		NodeId:             strPtr("node-existing"),
		WireguardPublicKey: &wgPub,
	})
	if err != nil {
		t.Fatalf("replay should succeed: %v", err)
	}
	if resp.GetNodeId() != "node-existing" {
		t.Fatalf("node_id mismatch: %s", resp.GetNodeId())
	}
	if resp.GetWireguardIp() != "10.88.0.5" {
		t.Fatalf("wireguard_ip mismatch: %s", resp.GetWireguardIp())
	}
	if resp.GetQuartermasterGrpcAddr() != "qm.internal:19002" {
		t.Fatalf("qm grpc addr missing: %s", resp.GetQuartermasterGrpcAddr())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestBootstrapInfrastructureNode_ReplayPubkeyMismatch refuses to replay
// when the stored public_key differs from the request — same node_id
// reused by a different keypair is a conflict, not a retry.
func TestBootstrapInfrastructureNode_ReplayPubkeyMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(cluster_id, ''\), expires_at, expected_ip::text`).
		WithArgs(hashBootstrapToken("tok")).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "expires_at", "expected_ip"}).
			AddRow("cluster-1", expiresAt, nil))
	mock.ExpectQuery(`SELECT\s+n\.cluster_id,\s+n\.wireguard_public_key`).
		WithArgs("node-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "wireguard_public_key", "wireguard_ip", "wireguard_listen_port", "owner_tenant_id"}).
			AddRow("cluster-1", "stored-pub", "10.88.0.5", int32(51820), nil))
	mock.ExpectRollback()

	wgPub := "different-pub"
	_, err = server.BootstrapInfrastructureNode(context.Background(), &quartermasterpb.BootstrapInfrastructureNodeRequest{
		Token:              "tok",
		NodeType:           "core",
		NodeId:             strPtr("node-existing"),
		WireguardPublicKey: &wgPub,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for pubkey mismatch, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestCollectBootstrapSeedIncludesCrossClusterQuartermaster(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	expectMeshRequirements(mock, "regional-1", "bridge")
	expectMeshEndpoints(mock, "regional", "regional-1", sqlmock.NewRows([]string{"type", "node_id", "wireguard_ip"}).
		AddRow("quartermaster", "central-1", "10.88.0.10").
		AddRow("purser", "central-1", "10.88.0.10"))
	expectMeshPeers(mock, "regional-1", "regional", sqlmock.NewRows([]string{"node_name", "wireguard_public_key", "external_ip", "internal_ip", "wireguard_ip", "wireguard_listen_port"}).
		AddRow("central-1", "central-pub", "203.0.113.10", nil, "10.88.0.10", int32(51820)))

	peers, endpoints := server.collectBootstrapSeed(context.Background(), "regional", "regional-1")
	if len(peers) != 1 || peers[0].GetNodeName() != "central-1" {
		t.Fatalf("expected cross-cluster central seed peer, got %#v", peers)
	}
	if got := endpoints["quartermaster"].GetIps(); len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("quartermaster endpoints = %v, want [10.88.0.10]", got)
	}
	if got := endpoints["purser"].GetIps(); len(got) != 1 || got[0] != "10.88.0.10" {
		t.Fatalf("purser endpoints = %v, want [10.88.0.10]", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
