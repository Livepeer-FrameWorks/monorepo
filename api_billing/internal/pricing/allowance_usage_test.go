package pricing

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/rating"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

func placementAllowanceFixture() *PlacementTariffSnapshot {
	start, end := snapshotObservedAt.Add(-time.Hour), snapshotObservedAt.Add(time.Hour)
	return &PlacementTariffSnapshot{TenantID: snapshotTenant, AsOf: snapshotObservedAt.Add(3 * time.Minute), Subscription: PlacementSubscriptionContext{PeriodStart: &start, PeriodEnd: &end}, Clusters: map[string]*ClusterPricing{"owned": {MeteredRules: []rating.Rule{{Meter: rating.MeterEgressGB, Model: rating.ModelTieredGraduated, IncludedQuantity: decimal.NewFromInt(100), UnitPrice: decimal.NewFromInt(1)}}}}}
}

func TestPlacementAllowanceCoverageControlsKnownZero(t *testing.T) {
	for _, test := range []struct {
		sources, missing, anomalies int64
		want                        PlacementUsageStatus
	}{
		{0, 0, 0, PlacementUsageMissingSources}, {1, 1, 0, PlacementUsageMissingWindows}, {1, 0, 1, PlacementUsageOpenAnomalies}, {1, 0, 0, PlacementUsageCovered},
	} {
		t.Run(string(test.want), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := placementAllowanceFixture()
			mock.ExpectQuery("ReadPlacementUsageCoverage").WillReturnRows(sqlmock.NewRows([]string{"active_sources", "missing_windows", "open_anomalies"}).AddRow(test.sources, test.missing, test.anomalies))
			if test.want == PlacementUsageCovered {
				mock.ExpectQuery("ListPlacementAllowanceUsage").WithArgs(snapshotObservedAt, *snapshot.Subscription.PeriodStart, snapshotTenant, pq.Array([]string{"owned"})).WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "quantity", "invalid_evidence"}))
			}
			usage, err := readPlacementAllowanceUsage(context.Background(), tx, snapshot, []string{"owned"})
			if err != nil || usage.Status != test.want || !usage.Through.Equal(snapshotObservedAt) || !usage.Through.Before(snapshot.AsOf) {
				t.Fatalf("coverage: %+v %v", usage, err)
			}
			if test.want == PlacementUsageCovered {
				if len(usage.Totals["owned"]) != 3 || !usage.Totals["owned"][rating.MeterEgressGB].IsZero() {
					t.Fatal("covered absence did not preserve explicit zero")
				}
			} else if usage.Totals != nil {
				t.Fatal("unknown coverage manufactured consumption")
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlacementAllowanceUnknownPeriodDoesNotQuery(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*PlacementTariffSnapshot)
		want   PlacementUsageStatus
	}{
		{"absent", func(s *PlacementTariffSnapshot) { s.Subscription = PlacementSubscriptionContext{} }, PlacementUsageUnknownPeriod},
		{"no full window", func(s *PlacementTariffSnapshot) {
			start := s.AsOf.Add(-time.Minute)
			s.Subscription.PeriodStart = &start
		}, PlacementUsageNoClosedWindow},
		{"unbounded", func(s *PlacementTariffSnapshot) {
			start := s.AsOf.Add(-400 * 24 * time.Hour)
			s.Subscription.PeriodStart = &start
		}, PlacementUsageUnknownPeriod},
		{"not needed", func(s *PlacementTariffSnapshot) { s.Clusters["owned"].MeteredRules[0].IncludedQuantity = decimal.Zero }, PlacementUsageNotRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := placementAllowanceFixture()
			test.mutate(snapshot)
			usage, err := readPlacementAllowanceUsage(context.Background(), nil, snapshot, []string{"owned"})
			if err != nil || usage.Status != test.want || usage.Totals != nil {
				t.Fatalf("period guessed: %+v %v", usage, err)
			}
		})
	}
}

func TestPlacementAllowanceExactAmountsAndInvalidEvidence(t *testing.T) {
	for _, test := range []struct {
		cluster, amount string
		invalid         bool
		status          PlacementUsageStatus
	}{
		{"owned", "9007199254740993.000001", false, PlacementUsageCovered},
		{"owned", "-0.000001", false, PlacementUsageInvalid},
		{"owned", "not-a-decimal", false, PlacementUsageInvalid},
		{"owned", "0", true, PlacementUsageInvalid},
		{"", "1", false, PlacementUsageUnattributed},
	} {
		t.Run(test.amount+test.cluster, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery("ReadPlacementUsageCoverage").WillReturnRows(sqlmock.NewRows([]string{"active_sources", "missing_windows", "open_anomalies"}).AddRow(1, 0, 0))
			mock.ExpectQuery("ListPlacementAllowanceUsage").WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "quantity", "invalid_evidence"}).AddRow(test.cluster, "egress_gb", test.amount, test.invalid))
			usage, err := readPlacementAllowanceUsage(context.Background(), tx, placementAllowanceFixture(), []string{"owned"})
			if test.amount == "not-a-decimal" {
				if err == nil || usage.Totals != nil {
					t.Fatal("malformed decimal did not abort the read")
				}
			} else if err != nil || usage.Status != test.status {
				t.Fatalf("usage %+v %v", usage, err)
			}
			if test.status == PlacementUsageCovered {
				if usage.Totals["owned"][rating.MeterEgressGB].String() != test.amount {
					t.Fatal("decimal quantity lost precision")
				}
			} else if usage.Totals != nil {
				t.Fatal("partial invalid usage escaped")
			}
			mock.ExpectRollback()
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
