package datamigrations

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

func TestRunTenantDNSEntitlementsBatchesCompatibilityBackfill(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT EXISTS").WithArgs(tenantDNSEntitlementHandoffKey).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("WITH batch AS").WithArgs(2).WillReturnResult(sqlmock.NewResult(0, 2))
	progress, err := runTenantDNSEntitlements(context.Background(), db, datamigrate.RunOptions{BatchSize: 2})
	if err != nil {
		t.Fatalf("runTenantDNSEntitlements: %v", err)
	}
	if progress.Scanned != 2 || progress.Changed != 2 || progress.Done {
		t.Fatalf("unexpected progress: %+v", progress)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunTenantDNSEntitlementsRefusesMissingSweepReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT EXISTS").WithArgs(tenantDNSEntitlementHandoffKey).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	_, err = runTenantDNSEntitlements(context.Background(), db, datamigrate.RunOptions{BatchSize: 2})
	if err == nil || !strings.Contains(err.Error(), "handoff incomplete") {
		t.Fatalf("runTenantDNSEntitlements error = %v, want incomplete handoff", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantDNSEntitlementsBackfillClaimsOnlyEpochAndStampsObservedState(t *testing.T) {
	if !containsAll(backfillTenantDNSEntitlementsSQL,
		"billing_entitlements_observed_at = 'epoch'",
		"billing_entitlements_observed_at = NOW()",
		"custom_subdomain_enabled = false",
		"custom_domain_enabled = false",
		"FOR UPDATE SKIP LOCKED",
	) {
		t.Fatal("backfill must claim only rows Purser did not observe, fail closed, and stamp them observed")
	}
	if !containsAll(tenantDNSEntitlementHandoffCompleteSQL,
		"billing_entitlement_handoffs",
		"handoff_key = $1",
	) {
		t.Fatal("closing migration must require Purser's durable full-sweep receipt")
	}
	if strings.Contains(tenantDNSEntitlementHandoffCompleteSQL, "COUNT(*) FROM quartermaster.tenants") {
		t.Fatal("immutable receipt must not be invalidated by post-handoff tenant creation")
	}
	if strings.Contains(backfillTenantDNSEntitlementsSQL, "deployment_tier IN") {
		t.Fatal("backfill must not infer billing entitlements from legacy tier names")
	}
}

func TestVerifyTenantDNSEntitlementsRejectsRemainingRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT COUNT\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	if err := verifyTenantDNSEntitlements(context.Background(), db); err == nil {
		t.Fatal("expected verification failure")
	}
}

func containsAll(value string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
}
