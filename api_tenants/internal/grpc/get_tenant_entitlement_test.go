package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
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

var effectiveAccessColumns = []string{
	"cluster_id", "cluster_name", "cluster_type", "base_url", "deployment_model",
	"owner_tenant_id", "cluster_class", "health_status", "access_level", "access_source",
	"access_active", "subscription_status", "access_expires_at",
}

func TestGetTenantEntitlement_ReturnsEffectiveAccess(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	const tenantID = "11111111-1111-1111-1111-111111111111"
	// Pin every load-bearing entitlement predicate and the bound tenant arg.
	mock.ExpectQuery(`(?s)tenant_cluster_access.*tenant_id = \$1::uuid.*is_active = true.*subscription_status = 'active'.*expires_at IS NULL OR tca\.expires_at > NOW\(\).*ic\.is_active = true`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows(effectiveAccessColumns).
			AddRow("cluster-a", "A", "edge", "a.example", "platform_managed", "", "standard", "healthy", "shared", "platform_tier", true, "active", nil).
			AddRow("cluster-b", "B", "edge", "b.example", "tenant_hosted_edge", tenantID, "private", "healthy", "dedicated", "owner", true, "active", nil))

	resp, err := server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: tenantID,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetAllowedClusterIds()) != 2 || resp.GetAllowedClusterIds()[0] != "cluster-a" {
		t.Fatalf("unexpected cluster IDs: %v", resp.GetAllowedClusterIds())
	}
	if len(resp.GetEffectiveAccess()) != 2 {
		t.Fatalf("unexpected effective access: %v", resp.GetEffectiveAccess())
	}
	if got := resp.GetEffectiveAccess()[0].GetAccessSource(); got != clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER {
		t.Fatalf("unexpected platform access source: %v", got)
	}
	if got := resp.GetEffectiveAccess()[1].GetAccessSource(); got != clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER {
		t.Fatalf("unexpected owner access source: %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetTenantEntitlement_NoEffectiveAccess(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery("tenant_cluster_access").
		WillReturnRows(sqlmock.NewRows(effectiveAccessColumns))

	resp, err := server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("expected empty successful response, got %v", err)
	}
	if len(resp.GetAllowedClusterIds()) != 0 || len(resp.GetEffectiveAccess()) != 0 {
		t.Fatalf("expected no access, got ids=%v access=%v", resp.GetAllowedClusterIds(), resp.GetEffectiveAccess())
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

func TestGetTenantEntitlement_ScanError_FailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	rows := sqlmock.NewRows(effectiveAccessColumns).
		AddRow("cluster-a", "A", "edge", "a.example", "platform_managed", "", "standard", "healthy", "shared", "platform_tier", true, "active", nil).
		RowError(0, errors.New("iter boom"))
	mock.ExpectQuery("tenant_cluster_access").WillReturnRows(rows)

	_, err = server.GetTenantEntitlement(serviceCtx(), &quartermasterpb.GetTenantEntitlementRequest{
		TenantId: "11111111-1111-1111-1111-111111111111",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal on row iteration error, got %v", err)
	}
}
