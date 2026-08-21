package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

var scannerTierID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// scanEntitlementRows reads tier_entitlements into a key→json-text map. The map
// is keyed by entitlement key so catalog diffing compares serialized values
// without reparsing.
func TestScanEntitlementRows(t *testing.T) {
	t.Run("maps rows", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.tier_entitlements WHERE tier_id = \$1`).
			WithArgs(scannerTierID).
			WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
				AddRow("recording_retention_days", []byte("30")).
				AddRow("max_concurrent_streams", []byte("5")))

		out := map[string]string{}
		if err := scanEntitlementRows(context.Background(), db, scannerTierID, out); err != nil {
			t.Fatalf("scanEntitlementRows: %v", err)
		}
		if out["recording_retention_days"] != "30" || out["max_concurrent_streams"] != "5" {
			t.Fatalf("entitlement map wrong: %+v", out)
		}
	})

	t.Run("query error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.tier_entitlements`).WillReturnError(errors.New("boom"))
		if err := scanEntitlementRows(context.Background(), db, scannerTierID, map[string]string{}); err == nil {
			t.Fatal("expected query error, got nil")
		}
	})
}

// scanPricingRuleRows reads tier_pricing_rules keyed by meter, preserving the
// NUMERIC columns as their text form for exact comparison against the catalog.
func TestScanPricingRuleRows(t *testing.T) {
	t.Run("maps rows", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.tier_pricing_rules WHERE tier_id = \$1`).
			WithArgs(scannerTierID).
			WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
				AddRow("delivered_minutes", "tiered_graduated", "EUR", "0.000000", "0.00055", []byte("{}")))

		out := map[string]currentRow{}
		if err := scanPricingRuleRows(context.Background(), db, scannerTierID, out); err != nil {
			t.Fatalf("scanPricingRuleRows: %v", err)
		}
		row, ok := out["delivered_minutes"]
		if !ok {
			t.Fatalf("missing meter key: %+v", out)
		}
		if row.unitPrice != "0.00055" || row.currency != "EUR" || row.model != "tiered_graduated" {
			t.Fatalf("pricing row wrong: %+v", row)
		}
	})

	t.Run("query error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).WillReturnError(errors.New("boom"))
		if err := scanPricingRuleRows(context.Background(), db, scannerTierID, map[string]currentRow{}); err == nil {
			t.Fatal("expected query error, got nil")
		}
	})
}

// loadPricedClusterIDs builds the set of clusters that already have a pricing
// row — the lookup ValidatePlatformOfficialPricingCoverage diffs against.
func TestLoadPricedClusterIDs(t *testing.T) {
	t.Run("builds set", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`SELECT cluster_id FROM purser\.cluster_pricing`).
			WillReturnRows(sqlmock.NewRows([]string{"cluster_id"}).
				AddRow("cluster-a").AddRow("cluster-b"))

		got, err := loadPricedClusterIDs(context.Background(), db)
		if err != nil {
			t.Fatalf("loadPricedClusterIDs: %v", err)
		}
		if !got["cluster-a"] || !got["cluster-b"] || got["cluster-x"] {
			t.Fatalf("priced set wrong: %+v", got)
		}
	})

	t.Run("query error", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.cluster_pricing`).WillReturnError(errors.New("boom"))
		if _, err := loadPricedClusterIDs(context.Background(), db); err == nil {
			t.Fatal("expected query error, got nil")
		}
	})
}

// A nil db is a programming error the validator must reject before attempting
// any Quartermaster dial.
func TestValidatePlatformOfficialPricingCoverageNilDB(t *testing.T) {
	_, err := ValidatePlatformOfficialPricingCoverage(context.Background(), nil, "qm:9000", "tok", nil)
	if err == nil {
		t.Fatal("expected error for nil db, got nil")
	}
}

// resolveTier maps a tier slug to its id/level/currency, defaults an empty
// currency, and gives a actionable error when the catalog hasn't been seeded.
func TestResolveTier(t *testing.T) {
	t.Run("maps row", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		tierID := "96000000-0000-4000-8000-000000000001"
		mock.ExpectQuery(`FROM purser\.billing_tiers[\s\S]+WHERE tier_name = \$1`).
			WithArgs("pro").
			WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow(tierID, int32(3), "USD"))

		id, level, currency, err := resolveTier(context.Background(), db, "pro")
		if err != nil {
			t.Fatalf("resolveTier: %v", err)
		}
		if id != tierID || level != 3 || currency != "USD" {
			t.Fatalf("resolved wrong: id=%s level=%d cur=%s", id, level, currency)
		}
	})

	t.Run("empty currency defaults", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.billing_tiers`).
			WithArgs("payg").
			WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow("96000000-0000-4000-8000-000000000002", int32(0), ""))

		_, _, currency, err := resolveTier(context.Background(), db, "payg")
		if err != nil {
			t.Fatalf("resolveTier: %v", err)
		}
		if currency == "" {
			t.Fatal("empty currency should default to the platform default")
		}
	})

	t.Run("unknown slug errors", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectQuery(`FROM purser\.billing_tiers`).
			WithArgs("ghost").
			WillReturnError(sql.ErrNoRows)

		if _, _, _, err := resolveTier(context.Background(), db, "ghost"); err == nil {
			t.Fatal("expected error for unknown tier slug, got nil")
		}
	})
}
