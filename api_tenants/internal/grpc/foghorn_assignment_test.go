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

func TestAssignFoghornToClusterCountFailsWhenNoRunningFoghornAvailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO quartermaster.foghorn_cluster_assignments").
		WithArgs("cluster-a", int32(1)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignFoghornToCluster(context.Background(), &pb.AssignFoghornToClusterRequest{
		ClusterId: "cluster-a",
		Count:     1,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestAssignFoghornToClusterInstanceIDFailsWhenInstanceMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("INSERT INTO quartermaster.foghorn_cluster_assignments[\\s\\S]*WHERE si.id = \\$2::uuid AND svc.type = 'foghorn' AND si.status = 'running'").
		WithArgs("cluster-a", "11111111-1111-1111-1111-111111111111").
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.AssignFoghornToCluster(context.Background(), &pb.AssignFoghornToClusterRequest{
		ClusterId:          "cluster-a",
		FoghornInstanceIds: []string{"11111111-1111-1111-1111-111111111111"},
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
