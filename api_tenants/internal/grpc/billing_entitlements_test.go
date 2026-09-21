package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestApplyTenantBillingEntitlementsRejectsUserBeforeStorage(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	_, err := server.ApplyTenantBillingEntitlements(tenantCtx("11111111-1111-4111-8111-111111111111", "owner"), &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: "11111111-1111-4111-8111-111111111111", DeploymentTier: "production", ObservedAt: timestamppb.Now(),
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("authorization touched storage: %v", err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffRequiresServiceAuthentication(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	_, err := server.CompleteTenantDNSEntitlementHandoff(tenantCtx("11111111-1111-4111-8111-111111111111", "owner"), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("status = %v, want PermissionDenied", status.Code(err))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("authorization touched storage: %v", err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffRecordsImmutableReceipt(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(tenantDNSEntitlementHandoffKey).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\)::bigint AS tenant_count`).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_count", "unobserved_count"}).AddRow(int64(3), int64(0)))
	mock.ExpectExec(`INSERT INTO quartermaster\.billing_entitlement_handoffs`).
		WithArgs(tenantDNSEntitlementHandoffKey, int64(3)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	resp, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetRecorded() {
		t.Fatal("new receipt was not reported as recorded")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffDoesNotFenceOnPagedCallerCount(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(tenantDNSEntitlementHandoffKey).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\)::bigint AS tenant_count`).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_count", "unobserved_count"}).AddRow(int64(4), int64(0)))
	mock.ExpectExec(`INSERT INTO quartermaster\.billing_entitlement_handoffs`).
		WithArgs(tenantDNSEntitlementHandoffKey, int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	resp, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if err != nil || !resp.GetRecorded() {
		t.Fatalf("mid-sweep signup handoff = recorded %v, err %v", resp.GetRecorded(), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffConfirmsExistingImmutableReceipt(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(tenantDNSEntitlementHandoffKey).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	resp, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 4})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetRecorded() {
		t.Fatal("existing durable receipt was not confirmed")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffRejectsIncompleteCensus(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(tenantDNSEntitlementHandoffKey).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\)::bigint AS tenant_count`).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_count", "unobserved_count"}).AddRow(int64(4), int64(1)))
	mock.ExpectRollback()

	_, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("status = %v, want FailedPrecondition: %v", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantDNSEntitlementHandoffReportsSerializableRetry(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(tenantDNSEntitlementHandoffKey).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COUNT\(\*\)::bigint AS tenant_count`).
		WillReturnError(&pq.Error{Code: "40001", Message: "restart transaction"})
	mock.ExpectRollback()

	_, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("status = %v, want Aborted: %v", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTenantBillingEntitlementsRejectsStaleObservation(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	observedAt := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT deployment_tier, custom_subdomain_enabled, custom_domain_enabled,`).
		WithArgs("11111111-1111-4111-8111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed_at",
		}).AddRow("production", true, true, observedAt.Add(time.Minute)))
	mock.ExpectRollback()

	resp, err := server.ApplyTenantBillingEntitlements(serviceCtx(), &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: "11111111-1111-4111-8111-111111111111", DeploymentTier: "free", ObservedAt: timestamppb.New(observedAt),
	})
	if err != nil {
		t.Fatalf("ApplyTenantBillingEntitlements: %v", err)
	}
	if resp.GetApplied() {
		t.Fatal("stale observation applied")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected stale-write side effect: %v", err)
	}
}

func TestApplyTenantBillingEntitlementsDoesNotMisclassifyMissingLockedUpdateAsNewerWriter(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	observedAt := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT deployment_tier, custom_subdomain_enabled, custom_domain_enabled,`).
		WithArgs("11111111-1111-4111-8111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed_at",
		}).AddRow("free", false, false, time.Unix(0, 0).UTC()))
	mock.ExpectExec(`UPDATE quartermaster\.tenants`).
		WithArgs(sql.NullString{String: "production", Valid: true}, true, true, observedAt, "11111111-1111-4111-8111-111111111111").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	_, err := server.ApplyTenantBillingEntitlements(serviceCtx(), &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: "11111111-1111-4111-8111-111111111111", DeploymentTier: "production", CustomSubdomainEnabled: true, CustomDomainEnabled: true,
		ObservedAt: timestamppb.New(observedAt),
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v, want Internal: %v", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyTenantBillingEntitlementsEnsuresAliasBeforeCustomDomain(t *testing.T) {
	server, _, mock := newMockQuartermasterServer(t)
	observedAt := time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT deployment_tier, custom_subdomain_enabled, custom_domain_enabled,`).
		WithArgs("11111111-1111-4111-8111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{
			"deployment_tier", "custom_subdomain_enabled", "custom_domain_enabled", "billing_entitlements_observed_at",
		}).AddRow("free", false, false, time.Unix(0, 0).UTC()))
	mock.ExpectExec(`UPDATE quartermaster\.tenants`).
		WithArgs(sql.NullString{String: "production", Valid: true}, true, true, observedAt, "11111111-1111-4111-8111-111111111111").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active.*FOR UPDATE`).
		WithArgs("11111111-1111-4111-8111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("Acme", "acme", true, true, observedAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("11111111-1111-4111-8111-111111111111", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("alias-1"))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).
		WithArgs("11111111-1111-4111-8111-111111111111").
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("video.acme.example", true, true, true, observedAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_custom_domain_outbox`).
		WithArgs("11111111-1111-4111-8111-111111111111", "video.acme.example", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("domain-1"))
	expectServiceEventOutbox(mock, eventTenantUpdated, "11111111-1111-4111-8111-111111111111")
	mock.ExpectCommit()

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	resp, err := server.ApplyTenantBillingEntitlements(ctx, &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: "11111111-1111-4111-8111-111111111111", DeploymentTier: "production",
		CustomSubdomainEnabled: true, CustomDomainEnabled: true, ObservedAt: timestamppb.New(observedAt),
	})
	if err != nil {
		t.Fatalf("ApplyTenantBillingEntitlements: %v", err)
	}
	if !resp.GetApplied() {
		t.Fatal("fresh entitlement observation was not applied")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("ordering/expectations: %v", err)
	}
}
