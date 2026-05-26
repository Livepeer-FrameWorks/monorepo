package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
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

	resp, err := server.ListClusters(ctx, &pb.ListClustersRequest{
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
