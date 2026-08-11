package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestListServiceClusterAssignments_RequiresServiceAuth(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.ListServiceClusterAssignments(context.Background(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		InstanceId:  "inst-1",
		ServiceType: "foghorn",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestListServiceClusterAssignments_MissingFields(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	if _, err := server.ListServiceClusterAssignments(serviceCtx(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		ServiceType: "foghorn",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing instance_id, got %v", err)
	}
	if _, err := server.ListServiceClusterAssignments(serviceCtx(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		InstanceId: "inst-1",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing service_type, got %v", err)
	}
}

func TestListServiceClusterAssignments_ReturnsClusterIDs(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// Pin the load-bearing predicates and both bound args so a regression that
	// drops status='running' / is_active / the instance/type filters can't stay green.
	mock.ExpectQuery(`(?s)service_cluster_assignments.*instance_id = \$1.*svc\.type = \$2.*status = 'running'.*is_active = true`).
		WithArgs("inst-1", "foghorn").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a").AddRow("cluster-b"))

	resp, err := server.ListServiceClusterAssignments(serviceCtx(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		InstanceId:  "inst-1",
		ServiceType: "foghorn",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetClusterIds()) != 2 || resp.GetClusterIds()[0] != "cluster-a" {
		t.Fatalf("unexpected cluster IDs: %v", resp.GetClusterIds())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListServiceClusterAssignments_QueryError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("service_cluster_assignments").WillReturnError(errors.New("boom"))

	if _, err := server.ListServiceClusterAssignments(serviceCtx(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		InstanceId:  "inst-1",
		ServiceType: "foghorn",
	}); status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestListServiceClusterAssignments_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	rows := sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a").RowError(0, errors.New("iter boom"))
	mock.ExpectQuery("service_cluster_assignments").WillReturnRows(rows)

	if _, err := server.ListServiceClusterAssignments(serviceCtx(), &quartermasterpb.ListServiceClusterAssignmentsRequest{
		InstanceId:  "inst-1",
		ServiceType: "foghorn",
	}); status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on row iteration error, got %v", err)
	}
}
