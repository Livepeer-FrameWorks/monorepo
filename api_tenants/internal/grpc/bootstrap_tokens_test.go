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

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"
)

func newMockQuartermasterServer(t *testing.T) (*QuartermasterServer, *sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &QuartermasterServer{db: db}, db, mock
}

func TestValidateBootstrapTokenConsumeRaceRejected(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1
	`)).
		WithArgs(hashBootstrapToken("bt_edge")).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "tenant_id", "cluster_id", "expected_ip", "expires_at", "usage_limit", "usage_count", "used_at"}).
			AddRow("edge_node", "tenant-1", "cluster-1", nil, expiresAt, nil, int32(0), nil))

	mock.ExpectExec(regexp.QuoteMeta(`
			UPDATE quartermaster.bootstrap_tokens
			SET usage_count = usage_count + 1, used_at = NOW()
			WHERE token_hash = $1
			  AND expires_at > NOW()
			  AND (
				(usage_limit IS NULL AND used_at IS NULL) OR
				(usage_limit IS NOT NULL AND usage_count < usage_limit)
			  )
		`)).
		WithArgs(hashBootstrapToken("bt_edge")).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := srv.ValidateBootstrapToken(context.Background(), &pb.ValidateBootstrapTokenRequest{
		Token:   "bt_edge",
		Consume: true,
	})
	if err != nil {
		t.Fatalf("ValidateBootstrapToken returned unexpected error: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("expected token consume race to return invalid response")
	}
	if got := resp.GetReason(); got != "already_used" {
		t.Fatalf("expected reason already_used, got %q", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateEnrollmentTokenRejectsCrossTenantRequest(t *testing.T) {
	srv, _, _ := newMockQuartermasterServer(t)

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-caller")
	_, err := srv.CreateEnrollmentToken(ctx, &pb.CreateEnrollmentTokenRequest{
		ClusterId: "cluster-1",
		TenantId:  ptr("tenant-other"),
	})
	if err == nil {
		t.Fatal("expected permission error for tenant mismatch")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

func TestBootstrapEdgeNode_ServedClusterValidation(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)

	tokenQuery := regexp.QuoteMeta(`
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`)

	t.Run("rejects token bound to unserved cluster", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(tokenQuery).
			WithArgs(hashBootstrapToken("tok-1")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
				AddRow("id-1", "tenant-1", "cluster-X", nil, int32(0), expiresAt, nil))
		mock.ExpectRollback()

		clusterA := "cluster-a"
		_, err := srv.BootstrapEdgeNode(context.Background(), &pb.BootstrapEdgeNodeRequest{
			Token:            "tok-1",
			Hostname:         "node-1",
			TargetClusterId:  &clusterA,
			ServedClusterIds: []string{"cluster-a", "cluster-b"},
		})
		if err == nil {
			t.Fatal("expected PermissionDenied for unserved cluster")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("expected PermissionDenied, got %v: %v", status.Code(err), err)
		}
	})

	t.Run("accepts token bound to served cluster", func(t *testing.T) {
		mock.ExpectBegin()
		mock.ExpectQuery(tokenQuery).
			WithArgs(hashBootstrapToken("tok-2")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
				AddRow("id-2", "tenant-1", "cluster-b", nil, int32(0), expiresAt, nil))
		// Node lookup (not found → insert)
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id`)).
			WillReturnError(sql.ErrNoRows)
		// Node insert (includes latitude, longitude from geoip)
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO quartermaster.infrastructure_nodes`)).
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "cluster-b", sqlmock.AnyArg(), nil, nil, nil).
			WillReturnResult(sqlmock.NewResult(0, 1))
		// Token update
		mock.ExpectExec(regexp.QuoteMeta(`UPDATE quartermaster.bootstrap_tokens`)).
			WithArgs(sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()

		clusterA := "cluster-a"
		resp, err := srv.BootstrapEdgeNode(context.Background(), &pb.BootstrapEdgeNodeRequest{
			Token:            "tok-2",
			Hostname:         "node-2",
			TargetClusterId:  &clusterA,
			ServedClusterIds: []string{"cluster-a", "cluster-b"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetClusterId() != "cluster-b" {
			t.Fatalf("expected cluster-b, got %q", resp.GetClusterId())
		}
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }
