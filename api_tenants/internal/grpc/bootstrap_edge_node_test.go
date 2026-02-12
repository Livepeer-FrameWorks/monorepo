package grpc

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestDeriveEdgeNodeID(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     string
	}{
		{name: "empty", hostname: "", want: ""},
		{name: "simple id", hostname: "abc123", want: "abc123"},
		{name: "fqdn", hostname: "edge-abc.cluster.example.com", want: "edge-abc"},
		{name: "trim and lowercase", hostname: "  EDGE-ABC  ", want: "edge-abc"},
		{name: "invalid char", hostname: "edge_abc", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveEdgeNodeID(tt.hostname); got != tt.want {
				t.Fatalf("deriveEdgeNodeID(%q) = %q, want %q", tt.hostname, got, tt.want)
			}
		})
	}
}

func TestBootstrapEdgeNode_UsesDerivedNodeIDFromHostname(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	srv := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil)
	expiresAt := time.Now().Add(1 * time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, tenant_id::text, COALESCE\(cluster_id, ''\), usage_limit, usage_count, expires_at, expected_ip::text\s+FROM quartermaster\.bootstrap_tokens\s+WHERE token = \$1 AND kind = 'edge_node'`).
		WithArgs("tok-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "cluster_id", "usage_limit", "usage_count", "expires_at", "expected_ip"}).
			AddRow("token-id", "tenant-1", "cluster-1", nil, int32(0), expiresAt, nil))
	mock.ExpectQuery(`SELECT cluster_id FROM quartermaster\.infrastructure_nodes WHERE node_id = \$1`).
		WithArgs("edge-abcd1234").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO quartermaster\.infrastructure_nodes \(id, node_id, cluster_id, node_name, node_type, external_ip, tags, metadata, created_at, updated_at\)`).
		WithArgs(sqlmock.AnyArg(), "edge-abcd1234", "cluster-1", "edge-abcd1234.example.com", nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE quartermaster\.bootstrap_tokens\s+SET usage_count = usage_count \+ 1, used_at = NOW\(\)\s+WHERE id = \$1`).
		WithArgs("token-id").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	resp, err := srv.BootstrapEdgeNode(t.Context(), &pb.BootstrapEdgeNodeRequest{
		Token:    "tok-1",
		Hostname: "edge-abcd1234.example.com",
	})
	if err != nil {
		t.Fatalf("BootstrapEdgeNode returned error: %v", err)
	}
	if resp.GetNodeId() != "edge-abcd1234" {
		t.Fatalf("expected node id edge-abcd1234, got %q", resp.GetNodeId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
