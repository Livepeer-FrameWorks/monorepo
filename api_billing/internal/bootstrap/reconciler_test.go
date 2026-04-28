package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// twoTierFixture is a minimal catalog for tests — full enough to exercise both
// JSONB and scalar columns without dragging the entire production catalog into
// every fixture.
func twoTierFixture() []CatalogTier {
	return []CatalogTier{
		{
			TierName:      "payg",
			DisplayName:   "Pay As You Go",
			Description:   "Prepaid pay-as-you-go.",
			BasePrice:     0,
			Currency:      "EUR",
			BillingPeriod: "monthly",
			BandwidthAllocation: map[string]any{
				"limit": 0, "unit": "delivered_minutes", "unit_price": 0.00049,
			},
			StorageAllocation: map[string]any{"limit": 0, "unit": "gb", "unit_price": 0.01},
			ComputeAllocation: map[string]any{"limit": 0, "unit": "gpu_hours", "unit_price": 0.5},
			Features:          map[string]any{"recording": true, "support_level": "community"},
			SupportLevel:      "community",
			SLALevel:          "none",
			MeteringEnabled:   true,
			OverageRates:      map[string]any{"bandwidth": map[string]any{"unit_price": 0.00049}},
			TierLevel:         0,
			IsDefaultPrepaid:  true,
			ProcessesLive:     `[{"process":"AV"}]`,
			ProcessesVOD:      `[{"process":"Thumbs"}]`,
		},
		{
			TierName:      "free",
			DisplayName:   "Free",
			Description:   "Self-hosted, no SLA.",
			BasePrice:     0,
			Currency:      "EUR",
			BillingPeriod: "monthly",
			BandwidthAllocation: map[string]any{
				"limit": nil, "unit": "delivered_minutes", "unit_price": 0,
			},
			StorageAllocation: map[string]any{"limit": 30, "unit": "retention_days", "unit_price": 0},
			ComputeAllocation: map[string]any{"limit": 0, "unit": "gpu_hours", "unit_price": 0},
			Features:          map[string]any{"recording": false, "support_level": "community"},
			SupportLevel:      "community",
			SLALevel:          "none",
			MeteringEnabled:   false,
			OverageRates:      map[string]any{},
			TierLevel:         1,
			IsDefaultPostpaid: true,
			ProcessesLive:     `[{"process":"AV"}]`,
			ProcessesVOD:      `[{"process":"Thumbs"}]`,
		},
	}
}

func TestEmbeddedCatalogParses(t *testing.T) {
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatalf("EmbeddedTiers: %v", err)
	}
	wantNames := []string{"payg", "free", "supporter", "developer", "production", "enterprise"}
	if got := len(tiers); got != len(wantNames) {
		t.Fatalf("expected %d tiers in catalog, got %d", len(wantNames), got)
	}
	have := map[string]bool{}
	for _, t := range tiers {
		have[t.TierName] = true
	}
	for _, n := range wantNames {
		if !have[n] {
			t.Errorf("expected tier %q in embedded catalog", n)
		}
	}

	for _, tt := range tiers {
		if tt.ProcessesLive == "" || tt.ProcessesVOD == "" {
			t.Errorf("tier %q missing MistServer process json", tt.TierName)
		}
	}
}

func TestReconcileBillingTierCatalogRejectsEmpty(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	if _, err := ReconcileBillingTierCatalog(context.Background(), db, nil); err == nil {
		t.Fatal("expected error on empty tier slice")
	}
}

func TestReconcileBillingTierCatalogRejectsNilDB(t *testing.T) {
	if _, err := ReconcileBillingTierCatalog(context.Background(), nil, twoTierFixture()); err == nil {
		t.Fatal("expected error on nil db")
	}
}

// TestReconcileBillingTierCatalogCreatesNewTiers covers the cold-start path: every
// tier is missing, every probe returns false, every row is inserted.
func TestReconcileBillingTierCatalogCreatesNewTiers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for _, tt := range twoTierFixture() {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE tier_name = $1)`)).
			WithArgs(tt.TierName).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectExec(`INSERT INTO purser\.billing_tiers`).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	res, err := ReconcileBillingTierCatalog(context.Background(), db, twoTierFixture())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Created); got != 2 {
		t.Fatalf("expected 2 created; got %d", got)
	}
	if len(res.Updated) != 0 || len(res.Noop) != 0 {
		t.Fatalf("expected only Created on cold start; got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestReconcileBillingTierCatalogIdempotent covers the second-run path: every
// tier exists, every comparison matches, no UPDATE issued. The result is all-noop,
// which CI can assert as the idempotency contract.
func TestReconcileBillingTierCatalogIdempotent(t *testing.T) {
	tiers := twoTierFixture()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	for _, tt := range tiers {
		mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE tier_name = $1)`)).
			WithArgs(tt.TierName).
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

		bandwidth, _ := jsonBytes(tt.BandwidthAllocation)
		storage, _ := jsonBytes(tt.StorageAllocation)
		compute, _ := jsonBytes(tt.ComputeAllocation)
		features, _ := jsonBytes(tt.Features)
		overage, _ := jsonBytes(tt.OverageRates)

		// The compare SELECT returns every column as text matching what the
		// reconciler would write — so the equality check passes and no UPDATE is
		// issued.
		mock.ExpectQuery(`SELECT[\s\S]*FROM purser\.billing_tiers[\s\S]*WHERE tier_name = \$1`).
			WithArgs(tt.TierName).
			WillReturnRows(sqlmock.NewRows([]string{
				"display_name", "description", "base_price", "currency", "billing_period",
				"bandwidth_allocation", "storage_allocation", "compute_allocation",
				"features", "support_level", "sla_level",
				"metering_enabled", "overage_rates",
				"tier_level", "is_enterprise",
				"is_default_prepaid", "is_default_postpaid",
				"processes_live", "processes_vod",
			}).AddRow(
				tt.DisplayName, tt.Description, fmtMoney(tt.BasePrice), tt.Currency, "monthly",
				string(bandwidth), string(storage), string(compute),
				string(features), tt.SupportLevel, tt.SLALevel,
				tt.MeteringEnabled, string(overage),
				tt.TierLevel, tt.IsEnterprise,
				tt.IsDefaultPrepaid, tt.IsDefaultPostpaid,
				processOrEmpty(tt.ProcessesLive), processOrEmpty(tt.ProcessesVOD),
			))
		// Deliberately no ExpectExec for UPDATE — a noop must not write.
	}

	res, err := ReconcileBillingTierCatalog(context.Background(), db, tiers)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Noop); got != 2 {
		t.Fatalf("expected 2 noop; got %d (created=%v updated=%v noop=%v)", got, res.Created, res.Updated, res.Noop)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestReconcileBillingTierCatalogUpdatesDriftedTier covers the drift path: the
// row exists but a mutable field has shifted. The reconciler emits an UPDATE.
func TestReconcileBillingTierCatalogUpdatesDriftedTier(t *testing.T) {
	tiers := twoTierFixture()[:1]
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	tt := tiers[0]
	bandwidth, _ := jsonBytes(tt.BandwidthAllocation)
	storage, _ := jsonBytes(tt.StorageAllocation)
	compute, _ := jsonBytes(tt.ComputeAllocation)
	features, _ := jsonBytes(tt.Features)
	overage, _ := jsonBytes(tt.OverageRates)

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE tier_name = $1)`)).
		WithArgs(tt.TierName).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// Compare returns drifted display_name — every other field still matches.
	mock.ExpectQuery(`SELECT[\s\S]*FROM purser\.billing_tiers[\s\S]*WHERE tier_name = \$1`).
		WithArgs(tt.TierName).
		WillReturnRows(sqlmock.NewRows([]string{
			"display_name", "description", "base_price", "currency", "billing_period",
			"bandwidth_allocation", "storage_allocation", "compute_allocation",
			"features", "support_level", "sla_level",
			"metering_enabled", "overage_rates",
			"tier_level", "is_enterprise",
			"is_default_prepaid", "is_default_postpaid",
			"processes_live", "processes_vod",
		}).AddRow(
			"OLD NAME (drifted)", tt.Description, fmtMoney(tt.BasePrice), tt.Currency, "monthly",
			string(bandwidth), string(storage), string(compute),
			string(features), tt.SupportLevel, tt.SLALevel,
			tt.MeteringEnabled, string(overage),
			tt.TierLevel, tt.IsEnterprise,
			tt.IsDefaultPrepaid, tt.IsDefaultPostpaid,
			processOrEmpty(tt.ProcessesLive), processOrEmpty(tt.ProcessesVOD),
		))
	mock.ExpectExec(`UPDATE purser\.billing_tiers`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileBillingTierCatalog(context.Background(), db, tiers)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Updated); got != 1 {
		t.Fatalf("expected 1 updated; got %d (full result %+v)", got, res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestReconcileBillingTierCatalogRollsBackOnError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM purser.billing_tiers WHERE tier_name = $1)`)).
		WithArgs("payg").
		WillReturnError(sql.ErrConnDone)

	if _, err := ReconcileBillingTierCatalog(context.Background(), db, twoTierFixture()); err == nil {
		t.Fatal("expected error from probe failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// fmtMoney reproduces the NUMERIC(10,2) text format Postgres emits, so test rows
// can hand the reconciler comparison values that match what production rows look
// like.
func fmtMoney(f float64) string {
	// Postgres formats DECIMAL(10,2) as e.g. "0.00", "999.00" — same as %.2f.
	return moneyText(f)
}
