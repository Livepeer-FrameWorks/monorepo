package handlers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// processPrepaidUsage gates deduction behind LoadEffectiveTier. These tests pin
// the orchestration short-circuits (no active sub, metering off, zero rated
// usage) without re-mocking the deduct transaction — that money-path is already
// covered by prepaid_credit_tx_test.go. With summary.ClusterID empty, the
// cluster-pricing resolver is skipped and tier rules apply directly.

const usagePeriod = "2026-04-01T00:00:00Z/2026-04-01T00:05:00Z"

func newPrepaidJM(t *testing.T) (*JobManager, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	return jm, mock, func() { _ = db.Close() }
}

// expectEffectiveTier mocks LoadEffectiveTier's 3 queries (subscription_id NULL
// → no override lookups): the tier header, tier_pricing_rules, tier_entitlements.
func expectEffectiveTier(mock sqlmock.Sqlmock, tenant, tierID string, metering bool, withRule bool) {
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts\s+JOIN purser\.billing_tiers bt`).
		WithArgs(tenant).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "sub_id"}).
			AddRow(tierID, "Pro", "79.00", billing.DefaultCurrency(), metering, nil))

	rules := sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"})
	if withRule {
		rules.AddRow("egress_gb", "all_usage", billing.DefaultCurrency(), "0", "0.010000", "{}")
	}
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules WHERE tier_id`).
		WithArgs(tierID).
		WillReturnRows(rules)

	mock.ExpectQuery(`FROM purser\.tier_entitlements WHERE tier_id`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
}

func TestProcessPrepaidUsage_NoActiveSubscription(t *testing.T) {
	jm, mock, done := newPrepaidJM(t)
	defer done()
	const tenant = "tenant-1"

	// LoadEffectiveTier's header query returns no row → treated as "no active
	// subscription" → skip silently (no deduction).
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts\s+JOIN purser\.billing_tiers bt`).
		WithArgs(tenant).
		WillReturnError(sql.ErrNoRows)

	err := jm.processPrepaidUsage(context.Background(),
		models.UsageSummary{TenantID: tenant, Period: usagePeriod}, nil)
	if err != nil {
		t.Fatalf("expected nil (skip), got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessPrepaidUsage_MeteringDisabled(t *testing.T) {
	jm, mock, done := newPrepaidJM(t)
	defer done()
	const tenant, tier = "tenant-1", "tier-pro"

	// Tier loads with metering disabled → returns before rating/deduction.
	expectEffectiveTier(mock, tenant, tier, false, false)

	err := jm.processPrepaidUsage(context.Background(),
		models.UsageSummary{TenantID: tenant, Period: usagePeriod}, nil)
	if err != nil {
		t.Fatalf("expected nil (metering off), got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessPrepaidUsage_ZeroRatedUsageSkipsDeduction(t *testing.T) {
	jm, mock, done := newPrepaidJM(t)
	defer done()
	const tenant, tier = "tenant-1", "tier-pro"

	// Metering on with a valid rule, but no accepted usage → rated UsageAmount
	// is zero → no deduction (no balance transaction queries expected).
	expectEffectiveTier(mock, tenant, tier, true, true)

	err := jm.processPrepaidUsage(context.Background(),
		models.UsageSummary{TenantID: tenant, Period: usagePeriod}, nil)
	if err != nil {
		t.Fatalf("expected nil (zero usage), got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestProcessPrepaidUsage_InvalidPeriod(t *testing.T) {
	jm, _, done := newPrepaidJM(t)
	defer done()
	// Malformed period fails before any DB access.
	err := jm.processPrepaidUsage(context.Background(),
		models.UsageSummary{TenantID: "tenant-1", Period: "not-a-period"}, nil)
	if err == nil {
		t.Fatal("expected error for malformed usage period")
	}
}
