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

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestBootstrapServiceDefersTokenConsumptionUntilSuccess(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs(hashBootstrapToken("token-1")).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).AddRow("service", "cluster-1", expiresAt))
	// ensureServiceExists mini-transaction
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	// IP reverse lookup (no match)
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "10.0.0.1").
		WillReturnError(sql.ErrNoRows)
	// back to main flow (idempotent check includes advertise_host)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000), "10.0.0.1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, nil, "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs(hashBootstrapToken("token-1")).
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

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs(hashBootstrapToken("token-1")).
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

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	expiresAt := time.Now().Add(30 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT kind, COALESCE\\(cluster_id, ''\\), expires_at").
		WithArgs(hashBootstrapToken("token-1")).
		WillReturnRows(sqlmock.NewRows([]string{"kind", "cluster_id", "expires_at"}).AddRow("service", "cluster-1", expiresAt))
	// ensureServiceExists mini-transaction
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	// IP reverse lookup (no match)
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "10.0.0.1").
		WillReturnError(sql.ErrNoRows)
	// back to main flow (idempotent check includes advertise_host)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000), "10.0.0.1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, nil, "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.bootstrap_tokens").
		WithArgs(hashBootstrapToken("token-1")).
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

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id,\\s+COALESCE\\(host\\(wireguard_ip\\), host\\(internal_ip\\), host\\(external_ip\\)\\)").
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

func TestBootstrapServiceClearMetadataReplacesExistingMetadataWithEmptyObject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "10.0.0.1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000), "10.0.0.1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("UPDATE quartermaster.service_instances").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, "{}", "uuid-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("bridge", "cluster-1", "inst-bridge-1234", "10.0.0.1", "http", int32(18000)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:          "bridge",
		ClusterId:     strPtr("cluster-1"),
		Port:          18000,
		Host:          "10.0.0.1",
		Protocol:      "http",
		Version:       "v1.0.0",
		Metadata:      map[string]string{"foo": "bar"},
		ClearMetadata: true,
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServicePoolServiceRegistersInPhysicalNodeCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("media-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id,\\s+COALESCE\\(host\\(wireguard_ip\\), host\\(internal_ip\\), host\\(external_ip\\)\\)").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "addr"}).AddRow("regional-eu-1", "10.1.0.10"))
	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("regional-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("foghorn").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("foghorn").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("foghorn"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("foghorn", "regional-eu-1", "http", int32(18008), "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "regional-eu-1", "node-1", "foghorn", "http", "10.1.0.10", "/health", "v1.0.0", int32(18008), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("media-eu-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("foghorn", "regional-eu-1", sqlmock.AnyArg(), "10.1.0.10", "http", int32(18008)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_nodes.*WHERE n\\.node_id = \\$1").
		WithArgs("node-1").
		WillReturnError(sql.ErrNoRows)

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "foghorn",
		ClusterId:      strPtr("media-eu-1"),
		NodeId:         strPtr("node-1"),
		Port:           18008,
		Protocol:       "http",
		HealthEndpoint: strPtr("/health"),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.GetClusterId() != "media-eu-1" || resp.GetNodeId() != "node-1" {
		t.Fatalf("unexpected bootstrap response: %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRejectsPoolServiceWithoutNodeID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("media-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "foghorn",
		ClusterId: strPtr("media-eu-1"),
		Port:      18008,
		Host:      "10.0.0.2",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceRejectsNonPoolServiceOnDifferentPhysicalNodeCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("core-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id,\\s+COALESCE\\(host\\(wireguard_ip\\), host\\(internal_ip\\), host\\(external_ip\\)\\)").
		WithArgs("regional-eu-2").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "addr"}).AddRow("regional-eu-1", "10.88.0.12"))
	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("regional-eu-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:      "bridge",
		ClusterId: strPtr("core-eu-1"),
		NodeId:    strPtr("regional-eu-2"),
		Port:      18000,
		Host:      "10.0.0.12",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceDerivesAdvertiseHostFromNodeID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id,\\s+COALESCE\\(host\\(wireguard_ip\\), host\\(internal_ip\\), host\\(external_ip\\)\\)").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "addr"}).AddRow("cluster-1", "10.88.0.2"))
	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("commodore").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("commodore").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("commodore"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("commodore", "cluster-1", "http", int32(18005), "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "cluster-1", "node-1", "commodore", "http", "10.88.0.2", "/health", "v1.0.0", int32(18005), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("commodore", "cluster-1", sqlmock.AnyArg(), "10.88.0.2", "http", int32(18005)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_nodes.*WHERE n\\.node_id = \\$1").
		WithArgs("node-1").
		WillReturnError(sql.ErrNoRows)

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "commodore",
		ClusterId:      strPtr("cluster-1"),
		NodeId:         strPtr("node-1"),
		AdvertiseHost:  strPtr("commodore"),
		Port:           18005,
		Protocol:       "http",
		HealthEndpoint: strPtr("/health"),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.GetAdvertiseAddr() != "10.88.0.2:18005" {
		t.Fatalf("expected mesh advertise addr, got %q", resp.GetAdvertiseAddr())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceUsesAdvertiseHostWhenNodeAddressIsLoopback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectQuery("SELECT cluster_id,\\s+COALESCE\\(host\\(wireguard_ip\\), host\\(internal_ip\\), host\\(external_ip\\)\\)").
		WithArgs("node-1").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "addr"}).AddRow("cluster-1", "127.0.0.1"))
	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("commodore").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("commodore").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("commodore"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("commodore", "cluster-1", "http", int32(18005), "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "cluster-1", "node-1", "commodore", "http", "commodore", "/health", "v1.0.0", int32(18005), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("UPDATE quartermaster.service_instances\\s+SET status = 'stopped'").
		WithArgs("commodore", "cluster-1", sqlmock.AnyArg(), "commodore", "http", int32(18005)).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("(?s)SELECT.*FROM quartermaster\\.infrastructure_nodes.*WHERE n\\.node_id = \\$1").
		WithArgs("node-1").
		WillReturnError(sql.ErrNoRows)

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "commodore",
		ClusterId:      strPtr("cluster-1"),
		NodeId:         strPtr("node-1"),
		AdvertiseHost:  strPtr("commodore"),
		Port:           18005,
		Protocol:       "http",
		HealthEndpoint: strPtr("/health"),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if resp.GetAdvertiseAddr() != "commodore:18005" {
		t.Fatalf("expected explicit advertise addr commodore:18005, got %q", resp.GetAdvertiseAddr())
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

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT is_active FROM quartermaster.infrastructure_clusters WHERE cluster_id = \\$1").
		WithArgs("cluster-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_active"}).AddRow(true))
	// ensureServiceExists mini-transaction
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	// IP reverse lookup (no match)
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "2001:db8::10").
		WillReturnError(sql.ErrNoRows)
	// back to main flow (idempotent check includes advertise_host)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(443), "2001:db8::10").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "cluster-1", nil, "bridge", "http", "2001:db8::10", (*string)(nil), "", int32(443), nil).
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

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	// ensureServiceExists mini-transaction
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	// IP reverse lookup (no match)
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "10.0.0.1").
		WillReturnError(sql.ErrNoRows)
	// back to main flow (idempotent check now includes advertise_host)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(18000), "10.0.0.1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "instance_id"}).AddRow("uuid-1", "inst-bridge-1234"))
	mock.ExpectExec("(?s)UPDATE quartermaster.service_instances.*stopped_at = NULL").
		WithArgs("10.0.0.1", sqlmock.AnyArg(), "v1.0.0", nil, nil, "uuid-1").
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

func TestBootstrapServiceSkipsIPLookupForHostname(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	// ensureServiceExists mini-transaction
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("quartermaster").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("quartermaster").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("quartermaster"))
	mock.ExpectCommit()
	// NO IP reverse lookup — "quartermaster" is a hostname, not an IP
	// back to main flow (idempotent check includes advertise_host)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("quartermaster", "cluster-1", "http", int32(18002), "quartermaster").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(sqlmock.AnyArg(), "cluster-1", nil, "quartermaster", "http", "quartermaster", "/health", "v1.0.0", int32(18002), nil).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE quartermaster.service_instances\s+SET status = 'stopped', stopped_at = NOW\(\)`).
		WithArgs("quartermaster", "cluster-1", sqlmock.AnyArg(), "quartermaster", "http", int32(18002)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "quartermaster",
		Port:           18002,
		AdvertiseHost:  strPtr("quartermaster"),
		Protocol:       "http",
		HealthEndpoint: strPtr("/health"),
		Version:        "v1.0.0",
	})
	if err != nil {
		t.Fatalf("expected success with hostname advertise_host, got error: %v", err)
	}
	if resp.GetAdvertiseAddr() != "quartermaster:18002" {
		t.Fatalf("expected hostname:port advertise addr, got %q", resp.GetAdvertiseAddr())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServicePoolServiceRequiresNodeIDBeforeLogicalAssignment(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "foghorn",
		Port:           9000,
		Host:           "10.0.0.2",
		Protocol:       "http",
		HealthEndpoint: strPtr(""),
		Version:        "v2.0.0",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

func TestBootstrapServiceStoresMetadataOnInsert(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	server := NewQuartermasterServer(db, logrus.New(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-1"))
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("bridge").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT service_id FROM quartermaster.services").
		WithArgs("bridge").
		WillReturnRows(sqlmock.NewRows([]string{"service_id"}).AddRow("bridge"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT node_id FROM quartermaster.infrastructure_nodes").
		WithArgs("cluster-1", "10.0.0.9").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT id::text, instance_id FROM quartermaster.service_instances").
		WithArgs("bridge", "cluster-1", "http", int32(8935), "10.0.0.9").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO quartermaster.service_instances").
		WithArgs(
			sqlmock.AnyArg(),
			"cluster-1",
			nil,
			"bridge",
			"http",
			"10.0.0.9",
			"/status",
			"cli-provisioned",
			int32(8935),
			sqlmock.AnyArg(),
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT owner_tenant_id FROM quartermaster.infrastructure_clusters").
		WithArgs("cluster-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`UPDATE quartermaster.service_instances\s+SET status = 'stopped', stopped_at = NOW\(\)`).
		WithArgs("bridge", "cluster-1", sqlmock.AnyArg(), "10.0.0.9", "http", int32(8935)).
		WillReturnResult(sqlmock.NewResult(0, 0))

	_, err = server.BootstrapService(context.Background(), &pb.BootstrapServiceRequest{
		Type:           "bridge",
		Port:           8935,
		Host:           "10.0.0.9",
		Protocol:       "http",
		HealthEndpoint: strPtr("/status"),
		Version:        "cli-provisioned",
		Metadata: map[string]string{
			"wallet_address": "0xabc123",
		},
	})
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
