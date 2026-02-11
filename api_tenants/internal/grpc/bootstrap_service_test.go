package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "frameworks/pkg/proto"
)

func TestBootstrapServiceDefersTokenConsumptionUntilSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs("token-1").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).AddRow("service", "cluster-1", expiresAt))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs("token-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("bridge", "cluster-1", "inst-bridge-1234", "10.0.0.1", "http", int32(18000)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "bridge",
		Token:          strPtr("token-1"),
		Port:           18000,
		Host:           "10.0.0.1",
		Protocol:       "http",
		HealthEndpoint: strPtr(""),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.GetInstanceId() != "inst-bridge-1234" {
		t.Fatalf("expected reused instance_id, got %s", resp.GetInstanceId())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRollbackDoesNotConsumeTokenOnValidationFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs("token-1").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).AddRow("service", "cluster-a", expiresAt))
	mock.ExpectRollback()

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "bridge",
		Token:     strPtr("token-1"),
		ClusterId: strPtr("cluster-b"),
		Port:      18000,
		Host:      "10.0.0.1",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRollbackWhenTokenAlreadyConsumed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs("token-1").
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).AddRow("service", "cluster-1", expiresAt))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs("token-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:     "bridge",
		Token:    strPtr("token-1"),
		Port:     18000,
		Host:     "10.0.0.1",
		Protocol: "http",
		Version:  "v1.0.0",
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
