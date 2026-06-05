package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPickFoghornControlCellCandidatePrefersNearestGeo(t *testing.T) {
	got := pickFoghornControlCellCandidate([]foghornControlCellCandidate{
		{
			instanceID:    "us-foghorn",
			controlCellID: "media-us-1",
			load:          0,
			latitude:      sql.NullFloat64{Float64: 37.7749, Valid: true},
			longitude:     sql.NullFloat64{Float64: -122.4194, Valid: true},
		},
		{
			instanceID:    "eu-foghorn",
			controlCellID: "media-eu-1",
			load:          9,
			latitude:      sql.NullFloat64{Float64: 52.3676, Valid: true},
			longitude:     sql.NullFloat64{Float64: 4.9041, Valid: true},
		},
	}, 52.37, 4.89, true)

	if got.controlCellID != "media-eu-1" {
		t.Fatalf("controlCellID = %q, want nearest EU control cell", got.controlCellID)
	}
}

func TestPickFoghornControlCellCandidateFallsBackToLoad(t *testing.T) {
	got := pickFoghornControlCellCandidate([]foghornControlCellCandidate{
		{instanceID: "loaded-foghorn", controlCellID: "media-eu-1", load: 10},
		{instanceID: "idle-foghorn", controlCellID: "media-us-1", load: 1},
	}, 0, 0, false)

	if got.controlCellID != "media-us-1" {
		t.Fatalf("controlCellID = %q, want least-loaded fallback", got.controlCellID)
	}
}

func TestAssignServiceToClusterCountFailsWhenNoRunningFoghornAvailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments \\(service_instance_id, cluster_id, source\\)[\\s\\S]*SELECT si.id, \\$1, 'runtime'[\\s\\S]*DO UPDATE SET is_active = true, updated_at = NOW\\(\\)").
		WithArgs("cluster-a", int32(1), "foghorn").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignServiceToCluster(context.Background(), &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId:   "cluster-a",
		Count:       1,
		ServiceType: "foghorn",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAssignServiceToClusterRequiresServiceType(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	_, err = server.AssignServiceToCluster(context.Background(), &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId: "cluster-a",
		Count:     1,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestAssignServiceToClusterInstanceIDFailsWhenInstanceMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments \\(service_instance_id, cluster_id, source\\)[\\s\\S]*SELECT si.id, \\$1, 'runtime'[\\s\\S]*WHERE si.id = \\$2::uuid AND svc.type = \\$3 AND si.status = 'running'[\\s\\S]*DO UPDATE SET is_active = true, updated_at = NOW\\(\\)").
		WithArgs("cluster-a", "11111111-1111-1111-1111-111111111111", "foghorn").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignServiceToCluster(context.Background(), &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId:   "cluster-a",
		InstanceIds: []string{"11111111-1111-1111-1111-111111111111"},
		ServiceType: "foghorn",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAssignServiceToClusterInstanceIDWritesRuntimeSourceAndPreservesOnConflict(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments \\(service_instance_id, cluster_id, source\\)[\\s\\S]*SELECT si.id, \\$1, 'runtime'[\\s\\S]*ON CONFLICT \\(service_instance_id, cluster_id\\) DO UPDATE SET is_active = true, updated_at = NOW\\(\\)").
		WithArgs("cluster-a", "11111111-1111-1111-1111-111111111111", "foghorn").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.AssignServiceToCluster(context.Background(), &quartermasterpb.AssignServiceToClusterRequest{
		ClusterId:   "cluster-a",
		InstanceIds: []string{"11111111-1111-1111-1111-111111111111"},
		ServiceType: "foghorn",
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestEnableSelfHostingAssignmentWritesRuntimeSource(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT max_owned_clusters, is_provider,").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"max_owned_clusters", "is_provider", "count"}).AddRow(10, true, 0))
	mock.ExpectQuery("SELECT pc.region_id").
		WithArgs("tenant-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT si.id::text AS instance_id").
		WillReturnRows(sqlmock.NewRows([]string{"instance_id", "control_cell", "control_region", "control_base_url", "load", "latitude", "longitude", "started_at"}).
			AddRow("11111111-1111-1111-1111-111111111111", "media-eu-1", "eu", "frameworks.network", int64(0), nil, nil, nil))
	mock.ExpectBegin()
	mock.ExpectExec("(?s)INSERT INTO quartermaster\\.infrastructure_clusters.*max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps.*VALUES.*0, 0, 0.*NULLIF\\(\\$8::text, ''\\), \\$2, 'tenant_private'").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "Tenant Edge", "tenant-1", nil, sqlmock.AnyArg(), "media-eu-1", "eu", "frameworks.network").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("(?s)INSERT INTO quartermaster\\.tenant_cluster_access.*VALUES").
		WithArgs("tenant-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments \\(service_instance_id, cluster_id, source\\)[\\s\\S]*SELECT si.id, \\$2, 'runtime'[\\s\\S]*ON CONFLICT \\(service_instance_id, cluster_id\\) DO UPDATE SET is_active = true, updated_at = NOW\\(\\)").
		WithArgs("11111111-1111-1111-1111-111111111111", sqlmock.AnyArg()).
		WillReturnError(errors.New("assignment failed"))
	mock.ExpectRollback()

	_, err = server.EnableSelfHosting(context.Background(), &quartermasterpb.EnableSelfHostingRequest{
		TenantId:    "tenant-1",
		ClusterName: "Tenant Edge",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestCreatePrivateClusterUsesUnlimitedCapacityDefaults(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT max_owned_clusters, is_provider,").
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"max_owned_clusters", "is_provider", "count"}).AddRow(10, true, 0))
	mock.ExpectQuery("SELECT si.id::text AS instance_id").
		WillReturnRows(sqlmock.NewRows([]string{"instance_id", "control_cell", "control_region", "control_base_url", "load", "latitude", "longitude", "started_at"}).
			AddRow("11111111-1111-1111-1111-111111111111", "media-eu-1", "eu", "frameworks.network", int64(0), nil, nil, nil))
	mock.ExpectBegin()
	mock.ExpectExec("(?s)INSERT INTO quartermaster\\.infrastructure_clusters.*max_concurrent_streams, max_concurrent_viewers, max_bandwidth_mbps.*VALUES.*0, 0, 0.*NULLIF\\(\\$8::text, ''\\), \\$2, 'tenant_private'").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "Tenant Edge", "tenant-1", nil, sqlmock.AnyArg(), "media-eu-1", "eu", "frameworks.network").
		WillReturnError(errors.New("stop after cluster insert"))
	mock.ExpectRollback()

	_, err = server.CreatePrivateCluster(context.Background(), &quartermasterpb.CreatePrivateClusterRequest{
		TenantId:    "tenant-1",
		ClusterName: "Tenant Edge",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
