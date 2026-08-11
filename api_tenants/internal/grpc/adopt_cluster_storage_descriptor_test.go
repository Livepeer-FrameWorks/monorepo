package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/lib/pq"
)

// adoptOperatorCtx is a platform-operator-authenticated context — AdoptClusterStorageDescriptor is a privileged
// operator action and refuses anything else (a service token is not operator authority).
func adoptOperatorCtx() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	return context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
}

// queryClusterColumns mirrors the 32-column SELECT list in queryCluster so the
// re-read after a successful adopt can be satisfied by sqlmock.
var queryClusterColumns = []string{
	"id", "cluster_id", "cluster_name", "cluster_type", "owner_tenant_id", "deployment_model",
	"base_url", "database_url", "periscope_url", "kafka_brokers",
	"max_concurrent_streams", "max_concurrent_viewers", "max_bandwidth_mbps",
	"health_status", "is_active", "is_default_cluster", "is_platform_official", "public_topology",
	"created_at", "updated_at",
	"visibility", "requires_approval", "short_description",
	"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix", "s3_prefix_present",
	"region_id", "cell_id", "cluster_class",
	"control_cell_id", "eligible_serving_cell_ids",
}

func queryClusterRow(clusterID, bucket, endpoint, region, prefix string) *sqlmock.Rows {
	now := time.Now()
	return sqlmock.NewRows(queryClusterColumns).AddRow(
		clusterID, clusterID, "Cluster One", "edge", nil, "shared",
		"https://one.example", nil, nil, pq.Array([]string{}),
		int32(0), int32(0), int32(0),
		"healthy", true, false, false, false,
		now, now,
		"private", false, nil,
		bucket, endpoint, region, prefix, prefix != "",
		"", "", "",
		"", pq.Array([]string{}),
	)
}

func newAdoptTestServer(t *testing.T) (*QuartermasterServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	return server, mock, func() { _ = db.Close() }
}

// Adopt onto an empty descriptor: bucket/endpoint/region/prefix are written and
// the cluster is re-read and returned.
func TestAdoptClusterStorageDescriptorFreshAdoption(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}).
			AddRow("", "", "", false, ""))
	mock.ExpectExec("s3_bucket = NULLIF").
		WithArgs("cluster-1", "bkt", "https://s3.example", "eu-west", "media").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("deployment_model").
		WithArgs("cluster-1").
		WillReturnRows(queryClusterRow("cluster-1", "bkt", "https://s3.example", "eu-west", "media"))

	resp, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId:  "cluster-1",
		S3Bucket:   "bkt",
		S3Endpoint: "https://s3.example",
		S3Region:   "eu-west",
		S3Prefix:   "media",
	})
	if err != nil {
		t.Fatalf("AdoptClusterStorageDescriptor: %v", err)
	}
	if resp.GetCluster().GetS3Bucket() != "bkt" || resp.GetCluster().GetS3Prefix() != "media" {
		t.Fatalf("re-read mismatch: %+v", resp.GetCluster())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// One-time prefix fill from NULL on an established descriptor: bucket matches,
// prefix is unset (NULL), so the fill is allowed.
func TestAdoptClusterStorageDescriptorPrefixOneTimeFill(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}).
			AddRow("bkt", "https://s3.example", "eu-west", false, ""))
	mock.ExpectExec("s3_bucket = NULLIF").
		WithArgs("cluster-1", "bkt", "https://s3.example", "eu-west", "media").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("deployment_model").
		WithArgs("cluster-1").
		WillReturnRows(queryClusterRow("cluster-1", "bkt", "https://s3.example", "eu-west", "media"))

	resp, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId:  "cluster-1",
		S3Bucket:   "bkt",
		S3Endpoint: "https://s3.example",
		S3Region:   "eu-west",
		S3Prefix:   "media",
	})
	if err != nil {
		t.Fatalf("prefix one-time fill: %v", err)
	}
	if resp.GetCluster().GetS3Prefix() != "media" {
		t.Fatalf("prefix not adopted: %+v", resp.GetCluster())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// A legacy row with an EMPTY (NULL) region and a deployed us-east-1 must NOT be a false repoint — the region is
// compared on its effective value and stored canonically. Prefix-only adoption on top succeeds.
func TestAdoptClusterStorageDescriptorEmptyRegionAdoptsUsEast1(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}).
			AddRow("bkt", "https://s3.example", "", false, "")) // region stored empty, prefix NULL
	// The effective region (us-east-1) is stored canonically.
	mock.ExpectExec("s3_bucket = NULLIF").
		WithArgs("cluster-1", "bkt", "https://s3.example", "us-east-1", "media").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("deployment_model").
		WithArgs("cluster-1").
		WillReturnRows(queryClusterRow("cluster-1", "bkt", "https://s3.example", "us-east-1", "media"))

	if _, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId: "cluster-1", S3Bucket: "bkt", S3Endpoint: "https://s3.example", S3Region: "us-east-1", S3Prefix: "media",
	}); err != nil {
		t.Fatalf("empty-region row adopting us-east-1 must succeed (not a repoint), got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Repointing an established bucket is refused with FailedPrecondition and no
// UPDATE is issued.
func TestAdoptClusterStorageDescriptorRepointRefused(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}).
			AddRow("bkt", "https://s3.example", "eu-west", true, "media"))
	mock.ExpectRollback()

	_, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId:  "cluster-1",
		S3Bucket:   "other-bkt",
		S3Endpoint: "https://s3.example",
		S3Region:   "eu-west",
		S3Prefix:   "media",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition on bucket repoint, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Repointing an already-adopted prefix is refused with FailedPrecondition.
func TestAdoptClusterStorageDescriptorPrefixRepointRefused(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}).
			AddRow("bkt", "https://s3.example", "eu-west", true, "media"))
	mock.ExpectRollback()

	_, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId:  "cluster-1",
		S3Bucket:   "bkt",
		S3Endpoint: "https://s3.example",
		S3Region:   "eu-west",
		S3Prefix:   "other-prefix",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition on prefix repoint, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// Only a verified platform-operator may adopt. A bare context AND a service-token context (the platform-wide token
// that carries no operator/cluster identity) are both refused before any DB work.
func TestAdoptClusterStorageDescriptorRequiresOperatorAuth(t *testing.T) {
	server, _, closeDB := newAdoptTestServer(t)
	defer closeDB()

	req := &quartermasterpb.AdoptClusterStorageDescriptorRequest{ClusterId: "cluster-1", S3Bucket: "bkt"}

	if _, err := server.AdoptClusterStorageDescriptor(context.Background(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for an unauthenticated caller, got %v", err)
	}

	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := server.AdoptClusterStorageDescriptor(serviceCtx, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for a service-token caller (not operator authority), got %v", err)
	}
}

func TestAdoptClusterStorageDescriptorNotFound(t *testing.T) {
	server, mock, closeDB := newAdoptTestServer(t)
	defer closeDB()

	// Empty result set makes the FOR UPDATE probe return sql.ErrNoRows.
	mock.ExpectBegin()
	mock.ExpectQuery("FOR UPDATE").
		WithArgs("missing").
		WillReturnRows(sqlmock.NewRows([]string{"s3_bucket", "s3_endpoint", "s3_region", "s3_prefix_set", "s3_prefix"}))
	mock.ExpectRollback()

	_, err := server.AdoptClusterStorageDescriptor(adoptOperatorCtx(), &quartermasterpb.AdoptClusterStorageDescriptorRequest{
		ClusterId: "missing",
		S3Bucket:  "bkt",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
