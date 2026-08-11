package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTenantEntitlement_RequiresServiceAuth(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.GetTenantEntitlement(context.Background(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestGetTenantEntitlement_MissingTenantID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	_, err = server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestGetTenantEntitlement_ReturnsClustersAndPlanClass(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	const tenantID = "11111111-1111-1111-1111-111111111111"
	// Pin the load-bearing entitlement predicates and the bound tenant arg so a
	// regression that drops is_active / subscription_status can't stay green.
	mock.ExpectQuery(`(?s)tenant_cluster_access.*tenant_id = \$1::uuid.*is_active = TRUE.*subscription_status = 'active'`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a").AddRow("cluster-b"))
	mock.ExpectQuery(`(?s)cluster_class.*infrastructure_clusters.*primary_cluster_id.*t\.id = \$1::uuid`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_class"}).AddRow("premium"))

	resp, err := server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: tenantID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetAllowedClusterIds()) != 2 || resp.GetAllowedClusterIds()[0] != "cluster-a" {
		t.Fatalf("unexpected cluster IDs: %v", resp.GetAllowedClusterIds())
	}
	if resp.GetPlanClass() != "premium" {
		t.Fatalf("expected plan_class premium, got %q", resp.GetPlanClass())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetTenantEntitlement_PlanClassNoRows_EmptyClassNoWarn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("tenant_cluster_access").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a"))
	// No tenant row → ErrNoRows on the plan-class lookup: empty class, no warn, bundle-equivalent success.
	mock.ExpectQuery("cluster_class").WillReturnError(sql.ErrNoRows)

	resp, err := server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("expected success on plan-class ErrNoRows, got %v", err)
	}
	if resp.GetPlanClass() != "" {
		t.Fatalf("expected empty plan_class on ErrNoRows, got %q", resp.GetPlanClass())
	}
	if len(resp.GetAllowedClusterIds()) != 1 {
		t.Fatalf("expected clusters preserved, got %v", resp.GetAllowedClusterIds())
	}
}

func TestGetTenantEntitlement_EntitlementQueryError_FailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("tenant_cluster_access").WillReturnError(errors.New("boom"))

	_, err = server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal, got %v", err)
	}
}

func TestGetTenantEntitlement_PlanClassError_FailsOpen(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("tenant_cluster_access").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a"))
	mock.ExpectQuery("cluster_class").WillReturnError(errors.New("boom"))

	resp, err := server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("expected success (fail-open on plan class), got %v", err)
	}
	if resp.GetPlanClass() != "" {
		t.Fatalf("expected empty plan_class on lookup error, got %q", resp.GetPlanClass())
	}
	if len(resp.GetAllowedClusterIds()) != 1 {
		t.Fatalf("expected clusters preserved, got %v", resp.GetAllowedClusterIds())
	}
}

func TestGetTenantEntitlement_ScanError_FailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	rows := sqlmock.NewRows([]string{"cluster_id"}).AddRow("cluster-a").RowError(0, errors.New("iter boom"))
	mock.ExpectQuery("tenant_cluster_access").WillReturnRows(rows)

	_, err = server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on row iteration error, got %v", err)
	}
}
