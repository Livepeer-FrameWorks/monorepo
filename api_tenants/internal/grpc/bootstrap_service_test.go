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
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, "uuid-1").
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
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, "uuid-1").
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

func TestBootstrapServiceRejectsMissingNodeReference(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id, COALESCE\\(wireguard_ip::text, internal_ip::text, external_ip::text\\)").
		WithArgs("node-missing").
		WillReturnError(sql.ErrNoRows)

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "bridge",
		ClusterId: strPtr("cluster-1"),
		NodeId:    strPtr("node-missing"),
		Port:      18000,
		Host:      "10.0.0.1",
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRejectsNodeFromDifferentCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-a").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id, COALESCE\\(wireguard_ip::text, internal_ip::text, external_ip::text\\)").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "addr"}).AddRow("cluster-b", "10.1.0.10"))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "bridge",
		ClusterId: strPtr("cluster-a"),
		NodeId:    strPtr("node-1"),
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

func TestBootstrapServiceFormatsIPv6AdvertiseAddr(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(443)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "cluster-1", nil, "bridge", "http", "2001:db8::10", (*string)(nil), "", int32(443)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("bridge", "cluster-1", sqlmock.AnyArg(), "2001:db8::10", "http", int32(443)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "bridge",
		ClusterId: strPtr("cluster-1"),
		Port:      443,
		Host:      "2001:db8::10",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.GetAdvertiseAddr() != "[2001:db8::10]:443" {
		t.Fatalf("expected bracketed IPv6 advertise addr, got %q", resp.GetAdvertiseAddr())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceReRegistrationClearsStoppedAt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("(?s)UPDATE quartermaster.service_instances.*stopped_at = NULL").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE quartermaster.service_instances\s+SET status = 'stopped', stopped_at = NOW\(\)`).
		WithArgs("bridge", "cluster-1", "inst-bridge-1234", "10.0.0.1", "http", int32(18000)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "bridge",
		Port:           18000,
		Host:           "10.0.0.1",
		Protocol:       "http",
		HealthEndpoint: strPtr(""),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceFoghornRemovesGhostAssignments(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("foghorn").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("foghorn"))
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("foghorn", "cluster-1", "http", int32(9000)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-2", "inst-foghorn-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.2", sqlmock.AnyArg(), "v2.0.0", nil, "uuid-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE quartermaster.service_instances\s+SET status = 'stopped', stopped_at = NOW\(\)`).
		WithArgs("foghorn", "cluster-1", "inst-foghorn-1234", "10.0.0.2", "http", int32(9000)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM quartermaster.foghorn_cluster_assignments").
		WithArgs("foghorn", "cluster-1", "inst-foghorn-1234").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO quartermaster.foghorn_cluster_assignments").
		WithArgs("cluster-1", "inst-foghorn-1234").
		WillReturnResult(sqlmock.NewResult(0, 1))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "foghorn",
		Port:           9000,
		Host:           "10.0.0.2",
		Protocol:       "http",
		HealthEndpoint: strPtr(""),
		Version:        "v2.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
