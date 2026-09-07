package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var clusterColumns = []string{
	"id", "cluster_id", "cluster_name", "cluster_type", "owner_tenant_id", "deployment_model",
	"base_url", "database_url", "periscope_url", "kafka_brokers",
	"max_concurrent_streams", "max_concurrent_viewers", "max_bandwidth_mbps",
	"health_status", "is_active", "is_default_cluster", "is_platform_official",
	"public_topology", "allow_private_pull_sources", "created_at", "updated_at",
}

func newClusterRow(id, clusterID, clusterName, clusterType string, isDefault bool, isPlatformOfficial bool) []driver.Value {
	now := time.Now()
	return []driver.Value{
		id,
		clusterID,
		clusterName,
		clusterType,
		nil,
		"managed",
		"frameworks.network",
		nil,
		nil,
		pq.StringArray{},
		int32(1000),
		int32(10000),
		int32(100000),
		"healthy",
		true,
		isDefault,
		isPlatformOfficial,
		false,
		false,
		now,
		now,
	}
}

func TestListClusters_PlatformOfficialFilterIgnoresTenantVisibility(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	isPlatformOfficial := true
	publicOfficialScope := `(?s)WHERE c\.is_platform_official = true`

	mock.ExpectQuery(publicOfficialScope).
		WithArgs().
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	mock.ExpectQuery(publicOfficialScope).
		WithArgs(51).
		WillReturnRows(sqlmock.NewRows(clusterColumns).
			AddRow(newClusterRow("uuid-1", "core-eu-1", "Core EU", "core", false, true)...).
			AddRow(newClusterRow("uuid-2", "media-eu-1", "Media EU", "edge", false, true)...))

	resp, err := server.ListClusters(ctx, &quartermasterpb.ListClustersRequest{
		IsPlatformOfficial: &isPlatformOfficial,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(resp.GetClusters()); got != 2 {
		t.Fatalf("expected 2 platform-official clusters, got %d", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListClustersRejectsTenantlessJWTOutsidePublicScope(t *testing.T) {
	server := &QuartermasterServer{logger: logging.NewLogger()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	_, err := server.ListClusters(ctx, &quartermasterpb.ListClustersRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("status = %v, want Unauthenticated", status.Code(err))
	}
}

func TestListMySubscriptionsMarksPrimaryClusterPreferred(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	const tenantID = "tenant-1"
	ctx := tenantCtx(tenantID, "member")

	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM quartermaster\.infrastructure_clusters c\s+WHERE c\.cluster_id IN`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))

	secretRow := newClusterRow("uuid-2", "cluster-preferred", "Tenant Preferred", "edge", true, true)
	secretRow[7] = "postgres://private"
	secretRow[8] = "https://periscope.private"
	secretRow[9] = pq.StringArray{"broker.private:9092"}
	mock.ExpectQuery(`(?s)AS is_default_cluster.*LEFT JOIN quartermaster\.tenants t ON t\.id = \$1`).
		WithArgs(tenantID, 51).
		WillReturnRows(sqlmock.NewRows(clusterColumns).
			AddRow(newClusterRow("uuid-1", "cluster-default", "Platform Default", "edge", false, true)...).
			AddRow(secretRow...))

	resp, err := server.ListMySubscriptions(ctx, &quartermasterpb.ListMySubscriptionsRequest{TenantId: tenantID})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(resp.GetClusters()); got != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", got)
	}
	if resp.GetClusters()[0].GetIsDefaultCluster() {
		t.Fatalf("expected platform default row to be false when tenant primary differs")
	}
	if !resp.GetClusters()[1].GetIsDefaultCluster() {
		t.Fatalf("expected tenant primary row to be marked preferred")
	}
	for _, cluster := range resp.GetClusters() {
		if cluster.DatabaseUrl != nil || cluster.PeriscopeUrl != nil || len(cluster.KafkaBrokers) != 0 {
			t.Fatalf("subscription leaked private endpoints: %+v", cluster)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

func TestListMySubscriptionsRejectsUnderScopedAPITokenBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	server := &QuartermasterServer{logger: logging.NewLogger()}
	_, err := server.ListMySubscriptions(ctx, &quartermasterpb.ListMySubscriptionsRequest{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
}
