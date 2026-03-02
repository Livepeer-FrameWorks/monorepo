package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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

	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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

	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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

	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiredAt, nil))
	mock.ExpectRollback()

	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("tok-1", "tenant-1", "cluster-a", nil, int32(0), expiresAt, nil))
	mock.ExpectRollback()

	targetCluster := "cluster-b"
	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil))

	// Idempotent check: node already exists in same cluster
	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_nodes").
		WithArgs("node-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	mock.ExpectRollback()

	resp, err := server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("tok-1", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil))

	// Node exists but in a different cluster
	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_nodes").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-other"))
	mock.ExpectRollback()

	_, err = server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
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
	// Token has no cluster binding
	mock.ExpectQuery("SELECT id, tenant_id::text").
		WithArgs(hashBootstrapToken("my-token")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("tok-1", "tenant-1", "", nil, int32(0), expiresAt, nil))

	// No target_cluster_id in request, so falls back to first active cluster
	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("fallback-cluster"))

	// Node doesn't exist yet
	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_nodes").
		WithArgs(sqlmock.AnyArg()). // auto-generated node-{uuid}
		WillReturnError(sql.ErrNoRows)

	// INSERT node
	mock.ExpectExec("INSERT INTO quartermaster.infrastructure_nodes").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "fallback-cluster", sqlmock.AnyArg(), "core", nil, nil, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Token usage update
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs("tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	mock.ExpectCommit()

	resp, err := server.BootstrapInfrastructureNode(context.Background(), &pb.BootstrapInfrastructureNodeRequest{
		Token:    "my-token",
		NodeType: "core",
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
