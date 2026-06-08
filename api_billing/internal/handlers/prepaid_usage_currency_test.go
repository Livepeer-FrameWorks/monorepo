package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// processPrepaidUsage must refuse to settle usage priced in a currency other
// than the prepaid balance's (the balance is single-currency). Reaching this
// guard requires metering enabled, and it must fire BEFORE any deduction —
// otherwise usage would be debited at the wrong currency's face value.
func TestProcessPrepaidUsage_CurrencyMismatchRejected(t *testing.T) {
	jm, mock, done := newPrepaidJM(t)
	defer done()
	const tenant, tier = "tenant-1", "tier-pro"

	other := "USD"
	if billing.DefaultCurrency() == other {
		other = "EUR"
	}

	// Tier header + rules both in the non-default currency (internally
	// consistent so LoadEffectiveTier accepts them), metering enabled.
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts\s+JOIN purser\.billing_tiers bt`).
		WithArgs(tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "sub_id"}).
			AddRow(tier, "Pro", "79.00", other, true, nil))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules WHERE tier_id`).
		WithArgs(tier).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("egress_gb", "all_usage", other, "0", "0.010000", "{}"))
	mock.ExpectQuery(`FROM purser\.tier_entitlements WHERE tier_id`).
		WithArgs(tier).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	err := jm.processPrepaidUsage(context.Background(),
		models.UsageSummary{TenantID: tenant, Period: usagePeriod}, nil)
	if err == nil {
		t.Fatal("expected currency-mismatch error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
