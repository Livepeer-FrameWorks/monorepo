package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/authz"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDelegatedAPITokenRequiresScopeAndOwnerRole(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	if err := requireTenantResourceActor(ctx, "tenant-1", authz.ActionManageEdgeCluster); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("missing scope status = %v", status.Code(err))
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"infrastructure:write"})
	if err := requireTenantResourceActor(ctx, "tenant-1", authz.ActionManageEdgeCluster); err != nil {
		t.Fatalf("scoped owner rejected: %v", err)
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "member")
	if err := requireTenantResourceActor(ctx, "tenant-1", authz.ActionManageEdgeCluster); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("scoped member status = %v", status.Code(err))
	}
}

func TestDelegatedTenantSettingsUseSettingsScope(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"infrastructure:write"})
	if err := requireTenantResourceActor(ctx, "tenant-1", authz.ActionManageTenantSettings); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("infrastructure-scoped token status = %v, want PermissionDenied", status.Code(err))
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"settings:write"})
	if err := requireTenantResourceActor(ctx, "tenant-1", authz.ActionManageTenantSettings); err != nil {
		t.Fatalf("settings-scoped owner rejected: %v", err)
	}
}

func TestAPITokenPermissionsAreExact(t *testing.T) {
	if hasAPITokenPermission([]string{"read"}, "streams:read") {
		t.Fatal("coarse read permission matched a namespaced read scope")
	}
	if hasAPITokenPermission([]string{"write"}, "infrastructure:write") {
		t.Fatal("coarse write permission matched a namespaced write scope")
	}
	if !hasAPITokenPermission([]string{" streams:read "}, "streams:read") {
		t.Fatal("exact permission was rejected")
	}
}

func TestMarketplaceReadBindsRequestedTenantToAuthenticatedTenant(t *testing.T) {
	ctx := tenantCtx("tenant-1", "member")
	if _, err := authorizeMarketplaceReadActor(ctx, "tenant-2"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign tenant status = %v, want PermissionDenied", status.Code(err))
	}
	if got, err := authorizeMarketplaceReadActor(ctx, ""); err != nil || got != "tenant-1" {
		t.Fatalf("implicit tenant = %q, err=%v", got, err)
	}

	apiCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	if _, err := authorizeMarketplaceReadActor(apiCtx, "tenant-1"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unscoped API token status = %v, want PermissionDenied", status.Code(err))
	}
	apiCtx = context.WithValue(apiCtx, ctxkeys.KeyPermissions, []string{"infrastructure:read"})
	if got, err := authorizeMarketplaceReadActor(apiCtx, "tenant-1"); err != nil || got != "tenant-1" {
		t.Fatalf("scoped API token tenant = %q, err=%v", got, err)
	}
}

func TestClusterLifecycleMutationsRejectMemberWithoutWrites(t *testing.T) {
	tests := []struct {
		name string
		call func(*QuartermasterServer) error
	}{
		{
			name: "generic create",
			call: func(server *QuartermasterServer) error {
				_, err := server.CreateCluster(tenantCtx("tenant-1", "member"), &quartermasterpb.CreateClusterRequest{ClusterId: "edge-1"})
				return err
			},
		},
		{
			name: "tenant delete",
			call: func(server *QuartermasterServer) error {
				_, err := server.DeleteTenant(tenantCtx("tenant-1", "member"), &quartermasterpb.DeleteTenantRequest{TenantId: "tenant-1"})
				return err
			},
		},
		{
			name: "marketplace update",
			call: func(server *QuartermasterServer) error {
				_, err := server.UpdateClusterMarketplace(tenantCtx("tenant-1", "member"), &quartermasterpb.UpdateClusterMarketplaceRequest{ClusterId: "edge-1", TenantId: "tenant-1"})
				return err
			},
		},
		{
			name: "metadata batch",
			call: func(server *QuartermasterServer) error {
				_, err := server.GetClusterMetadataBatch(tenantCtx("tenant-1", "member"), &quartermasterpb.GetClusterMetadataBatchRequest{ClusterIds: []string{"edge-1"}})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, mock := newMockQuartermasterServer(t)
			if err := test.call(server); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("denial touched storage: %v", err)
			}
		})
	}
}

func TestClusterLifecycleDenialDoesNotRevealForeignExistence(t *testing.T) {
	ctx := tenantCtx("tenant-1", "owner")
	call := func(queryResult *sqlmock.Rows, queryErr error) string {
		server, _, mock := newMockQuartermasterServer(t)
		expectation := mock.ExpectQuery(`SELECT owner_tenant_id[\s\S]*WHERE cluster_id = \$1`).WithArgs("edge-secret")
		if queryErr != nil {
			expectation.WillReturnError(queryErr)
		} else {
			expectation.WillReturnRows(queryResult)
		}
		active := false
		_, err := server.UpdateCluster(ctx, &quartermasterpb.UpdateClusterRequest{ClusterId: "edge-secret", IsActive: &active})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
		}
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Fatal(expectationErr)
		}
		return status.Convert(err).Message()
	}

	foreign := call(sqlmock.NewRows([]string{"owner_tenant_id"}).AddRow("tenant-2"), nil)
	missing := call(nil, sql.ErrNoRows)
	if foreign != privateInfrastructureDenied || missing != privateInfrastructureDenied {
		t.Fatalf("foreign=%q missing=%q, want common denial", foreign, missing)
	}
}

func TestMarketplaceUpdateDenialDoesNotRevealForeignExistence(t *testing.T) {
	ctx := tenantCtx("tenant-1", "owner")
	call := func(queryResult *sqlmock.Rows, queryErr error) string {
		server, _, mock := newMockQuartermasterServer(t)
		expectation := mock.ExpectQuery(`SELECT c.owner_tenant_id[\s\S]*WHERE c.cluster_id = \$1`).
			WithArgs("edge-secret", "tenant-1")
		if queryErr != nil {
			expectation.WillReturnError(queryErr)
		} else {
			expectation.WillReturnRows(queryResult)
		}
		description := "updated"
		_, err := server.UpdateClusterMarketplace(ctx, &quartermasterpb.UpdateClusterMarketplaceRequest{
			ClusterId:        "edge-secret",
			TenantId:         "tenant-1",
			ShortDescription: &description,
		})
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
		}
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Fatal(expectationErr)
		}
		return status.Convert(err).Message()
	}

	foreign := call(sqlmock.NewRows([]string{"owner_tenant_id", "is_provider"}).AddRow("tenant-2", false), nil)
	missing := call(nil, sql.ErrNoRows)
	if foreign != privateInfrastructureDenied || missing != privateInfrastructureDenied {
		t.Fatalf("foreign=%q missing=%q, want common denial", foreign, missing)
	}
}

func TestOwnedClusterMutationsRejectMemberBeforeWrite(t *testing.T) {
	tests := []struct {
		name string
		call func(*QuartermasterServer) error
	}{
		{
			name: "cluster update",
			call: func(server *QuartermasterServer) error {
				active := false
				_, err := server.UpdateCluster(tenantCtx("tenant-1", "member"), &quartermasterpb.UpdateClusterRequest{ClusterId: "edge-1", IsActive: &active})
				return err
			},
		},
		{
			name: "mesh update",
			call: func(server *QuartermasterServer) error {
				_, err := server.UpdateClusterMeshConfig(tenantCtx("tenant-1", "member"), &quartermasterpb.UpdateClusterMeshConfigRequest{ClusterId: "edge-1"})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, mock := newMockQuartermasterServer(t)
			mock.ExpectQuery(`SELECT owner_tenant_id[\s\S]*WHERE cluster_id = \$1`).
				WithArgs("edge-1").
				WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id"}).AddRow("tenant-1"))
			if err := test.call(server); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unexpected write or lookup: %v", err)
			}
		})
	}
}
