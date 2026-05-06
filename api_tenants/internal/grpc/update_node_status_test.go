package grpc

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func tenantCtx(tenantID, role string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenantID)
	if role != "" {
		ctx = context.WithValue(ctx, ctxkeys.KeyRole, role)
	}
	return ctx
}

func TestUpdateNodeStatus_RejectsTenantWithAccessButNotOwnership(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`UPDATE quartermaster\.infrastructure_nodes n[\s\S]*c\.owner_tenant_id = \$3[\s\S]*RETURNING n\.node_id`).
		WithArgs("edge-1", "retired", "00000000-0000-0000-0000-000000000001").
		WillReturnError(sql.ErrNoRows)

	_, err = server.UpdateNodeStatus(tenantCtx("00000000-0000-0000-0000-000000000001", ""), &pb.UpdateNodeStatusRequest{
		NodeId: "edge-1",
		Status: "retired",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("status code = %v, want NotFound from owner-scoped query", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateNodeStatus_AllowsProviderRoleAcrossActiveClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	clusterID := "cluster-1"
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT COALESCE(is_provider, false)
		FROM quartermaster.tenants
		WHERE id = $1
	`)).
		WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"is_provider"}).AddRow(true))
	mock.ExpectQuery(`UPDATE quartermaster\.infrastructure_nodes n[\s\S]*c\.is_active = true[\s\S]*RETURNING n\.node_id`).
		WithArgs("edge-1", "maintenance", clusterID).
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("edge-1"))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT id, node_id, cluster_id, node_name, node_type, internal_ip, external_ip,
			       wireguard_ip, wireguard_public_key, wireguard_listen_port, region, availability_zone,
			       latitude, longitude,
			       cpu_cores, memory_gb, disk_gb,
			       last_heartbeat, enrollment_origin, applied_mesh_revision, status, created_at, updated_at
			FROM quartermaster.infrastructure_nodes
			WHERE node_id = $1 OR id::text = $1`)).
		WithArgs("edge-1").
		WillReturnRows(sqlmock.NewRows(queryNodeColumns).AddRow(newNodeRow("uuid-1", "edge-1", clusterID, "edge-1", "edge", "203.0.113.10")...))

	resp, err := server.UpdateNodeStatus(tenantCtx("00000000-0000-0000-0000-000000000001", "provider"), &pb.UpdateNodeStatusRequest{
		NodeId:            "edge-1",
		Status:            "maintenance",
		ExpectedClusterId: &clusterID,
	})
	if err != nil {
		t.Fatalf("UpdateNodeStatus: %v", err)
	}
	if resp.GetNode().GetStatus() != "active" {
		t.Fatalf("node status = %q, want re-read node row", resp.GetNode().GetStatus())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateNodeStatus_RejectsTenantAdminAcrossActiveClusters(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	clusterID := "cluster-1"
	mock.ExpectQuery(`UPDATE quartermaster\.infrastructure_nodes n[\s\S]*c\.owner_tenant_id = \$4[\s\S]*RETURNING n\.node_id`).
		WithArgs("edge-1", "maintenance", clusterID, "00000000-0000-0000-0000-000000000001").
		WillReturnError(sql.ErrNoRows)

	_, err = server.UpdateNodeStatus(tenantCtx("00000000-0000-0000-0000-000000000001", "admin"), &pb.UpdateNodeStatusRequest{
		NodeId:            "edge-1",
		Status:            "maintenance",
		ExpectedClusterId: &clusterID,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("status code = %v, want NotFound from owner-scoped query", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
