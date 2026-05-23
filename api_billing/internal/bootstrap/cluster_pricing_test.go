package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func i32p(v int32) *int32 { return &v }
func bp(v bool) *bool     { return &v }

func samplePricing() ClusterPricing {
	return ClusterPricing{
		ClusterID:         "core-central-primary",
		PricingModel:      "tier_inherit",
		RequiredTierLevel: i32p(2),
		AllowFreeTier:     bp(false),
		BasePrice:         "0.00",
		Currency:          "EUR",
		MeteredRates:      map[string]any{},
		DefaultQuotas:     map[string]any{},
	}
}

func TestReconcileClusterPricingRejectsNilDB(t *testing.T) {
	if _, err := ReconcileClusterPricing(context.Background(), nil, []ClusterPricing{samplePricing()}); err == nil {
		t.Fatal("expected error on nil db")
	}
}

func TestReconcileClusterPricingRejectsInvalidModel(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bad := samplePricing()
	bad.PricingModel = "bogus"
	if _, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{bad}); err == nil {
		t.Fatal("expected error on invalid pricing_model")
	}
}

func TestReconcileClusterPricingRejectsMeterWithoutUnitPrice(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bad := samplePricing()
	bad.MeteredRates = map[string]any{
		"delivered_minutes": map[string]any{"model": "all_usage"},
	}
	if _, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{bad}); err == nil {
		t.Fatal("expected error on missing unit_price")
	}
}

func TestReconcileClusterPricingRejectsMeteredModelWithoutRates(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bad := samplePricing()
	bad.PricingModel = "metered"
	bad.MeteredRates = map[string]any{}
	if _, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{bad}); err == nil {
		t.Fatal("expected error on metered pricing without rates")
	}
}

func TestReconcileClusterPricingRejectsEmptyClusterID(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bad := samplePricing()
	bad.ClusterID = ""
	if _, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{bad}); err == nil {
		t.Fatal("expected error on empty cluster_id")
	}
}

func TestCheckRejectsMeterWithoutUnitPrice(t *testing.T) {
	bad := samplePricing()
	bad.MeteredRates = map[string]any{
		"delivered_minutes": map[string]any{"model": "all_usage"},
	}
	if err := Check(PurserSection{ClusterPricing: []ClusterPricing{bad}}, nil); err == nil {
		t.Fatal("expected check error on missing unit_price")
	}
}

func TestCheckRejectsMeteredModelWithoutRates(t *testing.T) {
	bad := samplePricing()
	bad.PricingModel = "custom"
	bad.MeteredRates = map[string]any{}
	if err := Check(PurserSection{ClusterPricing: []ClusterPricing{bad}}, nil); err == nil {
		t.Fatal("expected check error on custom pricing without rates")
	}
}

func TestReconcileClusterPricingCreatesWhenAbsent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery(`SELECT[\s\S]*FROM purser\.cluster_pricing[\s\S]*WHERE cluster_id = \$1`).
		WithArgs("core-central-primary").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.cluster_pricing`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{samplePricing()})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Created); got != 1 {
		t.Fatalf("expected 1 created; got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock: %v", err)
	}
}

func TestReconcileClusterPricingNoopWhenIdentical(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cp := samplePricing()
	metered, _ := jsonBytes(cp.MeteredRates)
	quotas, _ := jsonBytes(cp.DefaultQuotas)

	mock.ExpectQuery(`SELECT[\s\S]*FROM purser\.cluster_pricing[\s\S]*WHERE cluster_id = \$1`).
		WithArgs(cp.ClusterID).
		WillReturnRows(sqlmock.NewRows([]string{
			"pricing_model", "base_price", "currency",
			"required_tier_level", "allow_free_tier",
			"metered_rates", "default_quotas",
		}).AddRow(
			cp.PricingModel, "0.00", cp.Currency,
			int32(2), false,
			string(metered), string(quotas),
		))
	// No INSERT/UPDATE — must be noop.

	res, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{cp})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Noop); got != 1 {
		t.Fatalf("expected 1 noop; got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock: %v", err)
	}
}

func TestReconcileClusterPricingUpdatesOnDrift(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	cp := samplePricing()
	metered, _ := jsonBytes(cp.MeteredRates)
	quotas, _ := jsonBytes(cp.DefaultQuotas)

	mock.ExpectQuery(`SELECT[\s\S]*FROM purser\.cluster_pricing[\s\S]*WHERE cluster_id = \$1`).
		WithArgs(cp.ClusterID).
		WillReturnRows(sqlmock.NewRows([]string{
			"pricing_model", "base_price", "currency",
			"required_tier_level", "allow_free_tier",
			"metered_rates", "default_quotas",
		}).AddRow(
			"monthly", "9.99", cp.Currency, // drifted model + price
			int32(2), false,
			string(metered), string(quotas),
		))
	mock.ExpectExec(`UPDATE purser\.cluster_pricing`).WillReturnResult(sqlmock.NewResult(0, 1))

	res, err := ReconcileClusterPricing(context.Background(), db, []ClusterPricing{cp})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := len(res.Updated); got != 1 {
		t.Fatalf("expected 1 updated; got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock: %v", err)
	}
}

// regress against accidentally over-strict probe regex
var _ = regexp.MustCompile
