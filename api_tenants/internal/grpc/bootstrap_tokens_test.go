package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
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
		SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at, COALESCE(metadata, '{}'::jsonb)
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1
	`)).
		WithArgs(hashBootstrapToken("bt_edge")).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "tenant_id", "cluster_id", "expected_ip", "expires_at", "usage_limit", "usage_count", "used_at", "metadata"}).
			AddRow("edge_node", "tenant-1", "cluster-1", nil, expiresAt, nil, int32(0), nil, []byte(`{}`)))

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

	resp, err := srv.ValidateBootstrapToken(context.Background(), &quartermasterpb.ValidateBootstrapTokenRequest{
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
	_, err := srv.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{
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

func TestCreateEnrollmentTokenRejectsSubscriberAccess(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.infrastructure_clusters
				WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
				UNION
				SELECT 1 FROM quartermaster.tenant_cluster_access
				WHERE cluster_id = $1
				  AND tenant_id = $2
				  AND access_level = 'owner'
				  AND subscription_status = 'active'
				  AND is_active = true
			)
		`)).
		WithArgs("cluster-1", "tenant-subscriber").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-subscriber")
	_, err := srv.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{ClusterId: "cluster-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status code = %v, want PermissionDenied", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateEnrollmentTokenRejectsTenantAdminSubscriberAccess(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.infrastructure_clusters
				WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
				UNION
				SELECT 1 FROM quartermaster.tenant_cluster_access
				WHERE cluster_id = $1
				  AND tenant_id = $2
				  AND access_level = 'owner'
				  AND subscription_status = 'active'
				  AND is_active = true
			)
		`)).
		WithArgs("cluster-1", "tenant-subscriber").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-subscriber")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "admin")
	_, err := srv.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{ClusterId: "cluster-1"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status code = %v, want PermissionDenied", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateEnrollmentTokenAllowsOwnerAccess(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)

	mock.ExpectQuery(regexp.QuoteMeta(`
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.infrastructure_clusters
				WHERE cluster_id = $1 AND owner_tenant_id = $2 AND is_active = true
				UNION
				SELECT 1 FROM quartermaster.tenant_cluster_access
				WHERE cluster_id = $1
				  AND tenant_id = $2
				  AND access_level = 'owner'
				  AND subscription_status = 'active'
				  AND is_active = true
			)
		`)).
		WithArgs("cluster-1", "tenant-owner").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta(`
			INSERT INTO quartermaster.bootstrap_tokens (
				id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
			) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
		`)).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "Enrollment token for cluster-1", "tenant-owner", "cluster-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-owner")
	resp, err := srv.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{ClusterId: "cluster-1"})
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	if resp.GetToken().GetTenantId() != "tenant-owner" {
		t.Fatalf("tenant_id = %q, want tenant-owner", resp.GetToken().GetTenantId())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreateEnrollmentTokenRetriesRetryablePostgresError(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)

	mock.ExpectQuery(regexp.QuoteMeta(`
				SELECT EXISTS (
					SELECT 1 FROM quartermaster.infrastructure_clusters
					WHERE cluster_id = $1 AND is_active = true
				)
			`)).
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	insertQuery := regexp.QuoteMeta(`
			INSERT INTO quartermaster.bootstrap_tokens (
				id, token_hash, token_prefix, kind, name, tenant_id, cluster_id, expires_at, created_by, created_at
			) VALUES ($1, $2, $3, 'edge_node', $4, $5, $6, $7, $5, NOW())
		`)
	mock.ExpectExec(insertQuery).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "edge provision: edge-eu-1", "tenant-owner", "cluster-1", sqlmock.AnyArg()).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 92, got 91"})
	mock.ExpectExec(insertQuery).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "edge provision: edge-eu-1", "tenant-owner", "cluster-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := srv.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{
		ClusterId: "cluster-1",
		TenantId:  ptr("tenant-owner"),
		Name:      ptr("edge provision: edge-eu-1"),
	})
	if err != nil {
		t.Fatalf("CreateEnrollmentToken: %v", err)
	}
	if resp.GetToken().GetToken() == "" {
		t.Fatal("expected generated token")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestValidateBootstrapTokenRetriesRetryablePostgresError(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	expiresAt := time.Now().Add(time.Hour)
	tokenQuery := regexp.QuoteMeta(`
			SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at, COALESCE(metadata, '{}'::jsonb)
			FROM quartermaster.bootstrap_tokens
			WHERE token_hash = $1
		`)

	mock.ExpectQuery(tokenQuery).
		WithArgs(hashBootstrapToken("bt_edge")).
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 92, got 91"})
	mock.ExpectQuery(tokenQuery).
		WithArgs(hashBootstrapToken("bt_edge")).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "tenant_id", "cluster_id", "expected_ip", "expires_at", "usage_limit", "usage_count", "used_at", "metadata"}).
			AddRow("edge_node", "tenant-1", nil, nil, expiresAt, nil, int32(0), nil, []byte(`{}`)))

	resp, err := srv.ValidateBootstrapToken(context.Background(), &quartermasterpb.ValidateBootstrapTokenRequest{Token: "bt_edge"})
	if err != nil {
		t.Fatalf("ValidateBootstrapToken: %v", err)
	}
	if !resp.GetValid() {
		t.Fatalf("expected valid token, got reason %q", resp.GetReason())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestLookupClusterFoghornGRPCRetriesRetryablePostgresError(t *testing.T) {
	srv, _, mock := newMockQuartermasterServer(t)
	lookupQuery := regexp.QuoteMeta(`
			SELECT si.advertise_host || ':' || si.port
			FROM quartermaster.service_instances si
			JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id
			JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE sca.cluster_id = $1
			  AND sca.is_active = true
			  AND si.status = 'running'
			  AND si.health_status = 'healthy'
			  AND si.protocol = 'grpc'
			  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
			  AND svc.type = 'foghorn'
			ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, si.updated_at DESC, si.id ASC
			LIMIT 1
		`)

	mock.ExpectQuery(lookupQuery).
		WithArgs("media-eu-1").
		WillReturnError(&pq.Error{Code: "40001", Message: "schema version mismatch for table x: expected 92, got 91"})
	mock.ExpectQuery(lookupQuery).
		WithArgs("media-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"addr"}).AddRow("foghorn.internal:18019"))

	addr, err := srv.lookupClusterFoghornGRPC(context.Background(), "media-eu-1")
	if err != nil {
		t.Fatalf("lookupClusterFoghornGRPC: %v", err)
	}
	if addr != "foghorn.internal:18019" {
		t.Fatalf("addr = %q, want foghorn.internal:18019", addr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestPublicFoghornGRPCAddrUsesExternalEdgeListener(t *testing.T) {
	addr := publicFoghornGRPCAddr("media-eu-1", "https://frameworks.network/")
	if addr != "foghorn.media-eu-1.frameworks.network:18029" {
		t.Fatalf("addr = %q, want public Foghorn external listener", addr)
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
		_, err := srv.BootstrapEdgeNode(context.Background(), &quartermasterpb.BootstrapEdgeNodeRequest{
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
		resp, err := srv.BootstrapEdgeNode(context.Background(), &quartermasterpb.BootstrapEdgeNodeRequest{
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
