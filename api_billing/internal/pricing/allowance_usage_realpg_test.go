//go:build schema_verify

package pricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/rating"
)

func TestPlacementAllowanceUsage_RealPG(t *testing.T) {
	db := startPlacementSnapshotRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	asOf, err := purserdb.New(db).ReadPlacementSnapshotTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := asOf.UTC().Truncate(5 * time.Minute)
	start, end := cutoff.Add(-19*time.Minute), cutoff.Add(time.Hour)
	snapshot := placementAllowanceFixture()
	snapshot.AsOf, snapshot.Subscription.PeriodStart, snapshot.Subscription.PeriodEnd = asOf, &start, &end
	read := func(want PlacementUsageStatus) PlacementAllowanceUsage {
		t.Helper()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
				t.Log(rollbackErr)
			}
		}()
		usage, err := readPlacementAllowanceUsage(ctx, tx, snapshot, []string{"owned"})
		if err != nil || usage.Status != want {
			t.Fatalf("usage = %+v, %v; want %s", usage, err, want)
		}
		if want != PlacementUsageCovered && usage.Totals != nil {
			t.Fatal("incomplete evidence became consumption")
		}
		return usage
	}
	read(PlacementUsageMissingSources)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.metering_sources (source_id, active_from, active_until, required) VALUES ('placement-source', $1, $2, true)`, start, cutoff.Add(-2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := read(PlacementUsageMissingWindows); got.MissingWindows != 4 {
		t.Fatalf("partial source/period windows omitted: %+v", got)
	}
	for index := 0; index < 4; index++ {
		windowStart := cutoff.Add(time.Duration(-20+5*index) * time.Minute)
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.metering_windows (source_id, period_start, period_end, complete, report_count) VALUES ('placement-source', $1, $2, true, 1)`, windowStart, windowStart.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if index == 2 {
			if got := read(PlacementUsageMissingWindows); got.MissingWindows != 1 {
				t.Fatal("partial final source window considered complete")
			}
		}
	}
	if got := read(PlacementUsageCovered); len(got.Totals["owned"]) != 3 || !got.Totals["owned"][rating.MeterEgressGB].IsZero() {
		t.Fatal("covered empty ledger did not establish recorded zero")
	}
	for index, row := range []struct{ tenant, cluster, meter, unit, value, kind string }{
		{snapshotTenant, "owned", "egress_gb", "gibibyte", "2.000001", "delta"},
		{snapshotTenant, "owned", "egress_gb", "gibibyte", "3.000001", "delta"},
		{snapshotTenant, "owned", "egress_gb", "gibibyte", "100", "ignored"},
		{snapshotTenant, "other", "egress_gb", "gibibyte", "888", "delta"},
		{"85000000-0000-4000-8000-000000000001", "owned", "egress_gb", "gibibyte", "999", "delta"},
		{snapshotTenant, "owned", "storage_gb_seconds_hot", "gibibyte_second", "777", "delta"},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, unit, usage_value, value_kind, dimension_key, dimensions, source_id, report_id, period_start, period_end, granularity) VALUES ($1, $2, $3, $4, $5, $6, $7, '{"protocol":"hls"}', 'placement-source', $8, $9, $10, 'minute_5')`, row.tenant, row.cluster, row.meter, row.unit, row.value, row.kind, fmt.Sprintf("%064x", index), fmt.Sprintf("placement-%d", index), cutoff.Add(-20*time.Minute), cutoff.Add(-15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, correction := range []struct{ id, value, status string }{{"applied", "-0.000001", "applied"}, {"pending", "100", "pending"}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.usage_adjustments (tenant_id, cluster_id, usage_type, unit, delta_value, status, source_system, source_id, reason, period_start, period_end) VALUES ($1, 'owned', 'egress_gb', 'gibibyte', $2, $3, 'placement-test', $4, 'contract', $5, $6)`, snapshotTenant, correction.value, correction.status, correction.id, cutoff.Add(-20*time.Minute), cutoff.Add(-15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	usage := read(PlacementUsageCovered)
	if got := usage.Totals["owned"][rating.MeterEgressGB].String(); got != "5.000001" {
		t.Fatalf("corrections, exact decimals or attribution lost: %s", got)
	}
	if !usage.Through.Equal(cutoff) || usage.Through.After(asOf) {
		t.Fatal("usage fabricated reporting past its cutoff")
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.metering_anomalies (source_id, tenant_id, anomaly_type, source_event_id, created_at) VALUES ('placement-source', $1, 'contract', 'other-tenant', $2)`, "85000000-0000-4000-8000-000000000001", asOf.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	read(PlacementUsageCovered)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.metering_anomalies (source_id, anomaly_type, source_event_id, created_at) VALUES ('placement-source', 'contract', 'global', $1)`, asOf.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	read(PlacementUsageOpenAnomalies)
	if _, err := db.ExecContext(ctx, `UPDATE purser.metering_anomalies SET status = 'resolved' WHERE tenant_id IS NULL AND source_event_id = 'global'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.usage_records SET unit = 'byte' WHERE tenant_id = $1 AND report_id = 'placement-0'`, snapshotTenant); err != nil {
		t.Fatal(err)
	}
	read(PlacementUsageInvalid)
	if _, err := db.ExecContext(ctx, `UPDATE purser.usage_records SET unit = 'gibibyte', cluster_id = '' WHERE tenant_id = $1 AND report_id = 'placement-0'`, snapshotTenant); err != nil {
		t.Fatal(err)
	}
	read(PlacementUsageUnattributed)
}
