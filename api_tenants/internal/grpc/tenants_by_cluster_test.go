package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/lib/pq"
)

// Regression: the SELECT carries 17 columns + windowed total_count; a scan
// mismatch (official_cluster_id used to be selected but never scanned) made
// every row fail and the RPC silently return zero tenants.
func TestGetTenantsByClusterScansAllColumns(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	now := time.Now()
	rows := sqlmock.NewRows([]string{
		"id", "name", "subdomain", "custom_domain", "logo_url", "primary_color", "secondary_color",
		"deployment_tier", "deployment_model",
		"primary_cluster_id", "official_cluster_id", "kafka_topic_prefix", "kafka_brokers", "database_url",
		"is_active", "created_at", "updated_at", "total_count",
	}).AddRow(
		"tenant-1", "Acme", "acme", nil, nil, "#111111", "#222222",
		"pro", "shared",
		"cluster-eu", "cluster-official", nil, pq.Array([]string{}), nil,
		true, now, now, int32(7),
	)

	mock.ExpectQuery("FROM quartermaster.tenants t").
		WithArgs("cluster-eu", int32(2)).
		WillReturnRows(rows)

	resp, err := server.GetTenantsByCluster(context.Background(), &quartermasterpb.GetTenantsByClusterRequest{
		ClusterId:  "cluster-eu",
		Pagination: &commonpb.CursorPaginationRequest{First: 2},
	})
	if err != nil {
		t.Fatalf("GetTenantsByCluster: %v", err)
	}
	if len(resp.Tenants) != 1 {
		t.Fatalf("expected 1 tenant (scan must consume every selected column), got %d", len(resp.Tenants))
	}
	tenant := resp.Tenants[0]
	if tenant.Name != "Acme" || tenant.GetOfficialClusterId() != "cluster-official" {
		t.Fatalf("tenant mapping mismatch: %+v", tenant)
	}
	if resp.Pagination == nil || resp.Pagination.TotalCount != 7 || !resp.Pagination.HasNextPage {
		t.Fatalf("pagination mismatch: %+v", resp.Pagination)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetTenantsByClusterRejectsCursorPagination(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	after := "cursor"
	if _, err := server.GetTenantsByCluster(context.Background(), &quartermasterpb.GetTenantsByClusterRequest{
		ClusterId:  "cluster-eu",
		Pagination: &commonpb.CursorPaginationRequest{After: &after},
	}); err == nil {
		t.Fatal("cursor pagination is unsupported and must be rejected, not silently ignored")
	}
}

func TestGetTenantsByClusterFailsOnScanMismatch(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// One column short of the SELECT list: must surface as an error, never
	// as an empty tenant list.
	mock.ExpectQuery("FROM quartermaster.tenants t").
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}).AddRow("tenant-1", "Acme"))

	if _, err := server.GetTenantsByCluster(context.Background(), &quartermasterpb.GetTenantsByClusterRequest{
		ClusterId: "cluster-eu",
	}); err == nil {
		t.Fatal("scan mismatch must be a hard error")
	}
}

func TestGetTenantsByClusterRequiresClusterID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	if _, err := server.GetTenantsByCluster(context.Background(), &quartermasterpb.GetTenantsByClusterRequest{}); err == nil {
		t.Fatal("expected InvalidArgument for missing cluster_id")
	}
}
