package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func newTestTier(currency string) *billingpkg.EffectiveTier {
	return &billingpkg.EffectiveTier{
		TierID:          "tier-test",
		TierName:        "test-tier",
		Currency:        currency,
		BasePrice:       dec("79.00"),
		MeteringEnabled: true,
	}
}

func TestRateInvoiceForTenantZeroesBaseForProviderManagedSub(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
	periodStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	got, err := jm.rateInvoiceForTenant(
		context.Background(),
		"tenant-stripe",
		periodStart, periodEnd,
		newTestTier("EUR"),
		true,                            // includeBasePrice
		true,                            // baseProviderManaged — Stripe/Mollie owns the base
		map[string]map[string]float64{}, // no usage → no resolver call
		map[string][]rating.DimensionedQuantity{},
	)
	if err != nil {
		t.Fatalf("rateInvoiceForTenant: %v", err)
	}

	if !got.BaseAmount.IsZero() {
		t.Errorf("BaseAmount = %s, want 0.00 (provider-managed base must not double-bill)", got.BaseAmount)
	}
	if !got.BaseLine.Amount.IsZero() || !got.BaseLine.UnitPrice.IsZero() {
		t.Errorf("BaseLine money fields not zeroed: amount=%s unit=%s", got.BaseLine.Amount, got.BaseLine.UnitPrice)
	}
	if got.BaseLine.PricingSource != pricing.SourceIncludedSubscription {
		t.Errorf("BaseLine.PricingSource = %s, want %s", got.BaseLine.PricingSource, pricing.SourceIncludedSubscription)
	}
}

func TestRateInvoiceForTenantKeepsBaseForSelfManagedSub(t *testing.T) {
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
	periodStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	got, err := jm.rateInvoiceForTenant(
		context.Background(),
		"tenant-self",
		periodStart, periodEnd,
		newTestTier("EUR"),
		true,                            // includeBasePrice
		false,                           // baseProviderManaged — Purser owns the base
		map[string]map[string]float64{}, // no usage
		map[string][]rating.DimensionedQuantity{},
	)
	if err != nil {
		t.Fatalf("rateInvoiceForTenant: %v", err)
	}

	if !got.BaseAmount.Equal(dec("79.00")) {
		t.Errorf("BaseAmount = %s, want 79.00 (self-managed must charge base)", got.BaseAmount)
	}
	if got.BaseLine.PricingSource != pricing.SourceTier {
		t.Errorf("BaseLine.PricingSource = %s, want %s", got.BaseLine.PricingSource, pricing.SourceTier)
	}
}

func TestFreeStatementPreservesUsageAtZeroDue(t *testing.T) {
	t.Setenv("WAIVE_USAGE_CHARGES", "false")
	mockDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	tier := &billingpkg.EffectiveTier{
		TierID: "free-tier", TierName: "free", Currency: "EUR",
		BasePrice: dec("0"), MeteringEnabled: true,
		Rules: []rating.Rule{{
			Meter: rating.MeterDeliveredMinutes, Model: rating.ModelAllUsage,
			Currency: "EUR", UnitPrice: dec("0"),
		}},
	}
	jm := &JobManager{db: mockDB, logger: logging.NewLogger(), billing: &Service{}}
	periodStart := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	result, err := jm.rateInvoiceForTenant(
		context.Background(), "tenant-free", periodStart, periodStart.AddDate(0, 1, 0), tier,
		true, false,
		map[string]map[string]float64{"": {string(rating.MeterDeliveredMinutes): 120}},
		map[string][]rating.DimensionedQuantity{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TotalAmount.IsZero() || !result.UsageAmount.IsZero() || !result.GrossUsageAmount.IsZero() {
		t.Fatalf("Free totals = net:%s usage:%s gross:%s", result.TotalAmount, result.UsageAmount, result.GrossUsageAmount)
	}
	if len(result.UsageLines) != 1 || !result.UsageLines[0].Quantity.Equal(dec("120")) || !result.UsageLines[0].Amount.IsZero() {
		t.Fatalf("Free itemized usage = %+v", result.UsageLines)
	}
	if status := finalizedInvoiceStatus(result.TotalAmount); status != "paid" {
		t.Fatalf("zero-due Free statement status = %q, want paid", status)
	}
}
