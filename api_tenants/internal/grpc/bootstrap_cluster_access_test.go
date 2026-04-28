package grpc

import (
	"context"
	"regexp"
	"testing"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
)

func serviceCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
}

func TestBootstrapClusterAccess_RequiresServiceToken(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// No auth_type set on context.
	if _, err := server.BootstrapClusterAccess(context.Background(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000001", ClusterId: "core-1",
	}); err == nil {
		t.Fatal("expected PermissionDenied for non-service-token caller")
	}
}

func TestBootstrapClusterAccess_ValidatesArgs(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		ClusterId: "core-1",
	}); err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	}); err == nil {
		t.Fatal("expected error for missing cluster_id")
	}
}

func TestBootstrapClusterAccess_RejectsUnknownTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1::uuid)")).
		WithArgs("00000000-0000-0000-0000-000000000099").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000099", ClusterId: "core-1",
	}); err == nil {
		t.Fatal("expected NotFound for unknown tenant")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestBootstrapClusterAccess_RejectsNonPlatformOfficial(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1::uuid)")).
		WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_platform_official, is_active")).
		WithArgs("private-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_platform_official", "is_active"}).AddRow(false, true))

	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000001", ClusterId: "private-1",
	}); err == nil {
		t.Fatal("expected FailedPrecondition for non-platform-official cluster")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestBootstrapClusterAccess_RejectsInactiveCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1::uuid)")).
		WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_platform_official, is_active")).
		WithArgs("retired-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_platform_official", "is_active"}).AddRow(true, false))

	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000001", ClusterId: "retired-1",
	}); err == nil {
		t.Fatal("expected FailedPrecondition for inactive cluster")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestBootstrapClusterAccess_UpsertsOnHappyPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS(SELECT 1 FROM quartermaster.tenants WHERE id = $1::uuid)")).
		WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT is_platform_official, is_active")).
		WithArgs("core-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_platform_official", "is_active"}).AddRow(true, true))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO quartermaster.tenant_cluster_access")).
		WithArgs("00000000-0000-0000-0000-000000000001", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := server.BootstrapClusterAccess(serviceCtx(), &pb.BootstrapClusterAccessRequest{
		TenantId: "00000000-0000-0000-0000-000000000001", ClusterId: "core-1",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
