package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

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
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments").
		WithArgs("cluster-a", int32(1), "foghorn").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignServiceToCluster(context.Background(), &pb.AssignServiceToClusterRequest{
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

	_, err = server.AssignServiceToCluster(context.Background(), &pb.AssignServiceToClusterRequest{
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
	mock.ExpectExec("INSERT INTO quartermaster.service_cluster_assignments[\\s\\S]*WHERE si.id = \\$2::uuid AND svc.type = \\$3 AND si.status = 'running'").
		WithArgs("cluster-a", "11111111-1111-1111-1111-111111111111", "foghorn").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignServiceToCluster(context.Background(), &pb.AssignServiceToClusterRequest{
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
