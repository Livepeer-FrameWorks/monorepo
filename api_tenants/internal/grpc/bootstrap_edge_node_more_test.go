package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

func TestBootstrapEdgeNode_IdempotentWhenExistingClusterMatches(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`)).
		WithArgs("tok-idempotent").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("token-id", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1`)).
		WithArgs("edge-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	mock.ExpectCommit()

	resp, err := srv.BootstrapEdgeNode(context.Background(), &pb.BootstrapEdgeNodeRequest{
		Token:    "tok-idempotent",
		Hostname: "edge-existing.example.com",
	})
	if err != nil {
		t.Fatalf("BootstrapEdgeNode returned error: %v", err)
	}
	if resp.GetNodeId() != "edge-existing" {
		t.Fatalf("expected existing node id edge-existing, got %q", resp.GetNodeId())
	}
	if resp.GetClusterId() != "cluster-1" {
		t.Fatalf("expected cluster-1, got %q", resp.GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapEdgeNode_RejectsWhenExistingNodeInOtherCluster(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`)).
		WithArgs("tok-conflict").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("token-id", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil))

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1`)).
		WithArgs("edge-existing").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-2"))
	mock.ExpectRollback()

	_, err := srv.BootstrapEdgeNode(context.Background(), &pb.BootstrapEdgeNodeRequest{
		Token:    "tok-conflict",
		Hostname: "edge-existing.example.com",
	})
	if err == nil {
		t.Fatal("expected failed precondition for conflicting cluster")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v: %v", status.Code(err), err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestBootstrapEdgeNode_UsesFallbackActiveCluster(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`)).
		WithArgs("tok-fallback").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("token-id", "tenant-1", "", nil, int32(0), expiresAt, nil))

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT cluster_id FROM quartermaster.infrastructure_clusters
			WHERE is_active = true
			ORDER BY cluster_name LIMIT 1
		`)).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-fallback"))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1`)).
		WithArgs("edge-fallback").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, tags, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'edge', $5::inet, '{}', '{}', NOW(), NOW())
	`)).
		WithArgs(sqlmock.AnyArg(), "edge-fallback", "cluster-fallback", "edge-fallback.example.com", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(`
		UPDATE quartermaster.bootstrap_tokens
		SET usage_count = usage_count + 1, used_at = NOW()
		WHERE id = $1
	`)).
		WithArgs("token-id").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	resp, err := srv.BootstrapEdgeNode(context.Background(), &pb.BootstrapEdgeNodeRequest{
		Token:    "tok-fallback",
		Hostname: "edge-fallback.example.com",
	})
	if err != nil {
		t.Fatalf("BootstrapEdgeNode returned error: %v", err)
	}
	if resp.GetClusterId() != "cluster-fallback" {
		t.Fatalf("expected fallback cluster, got %q", resp.GetClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
