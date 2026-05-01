package stripe

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

// TestEnqueueMeterEvents_ManualReviewIsHardHold verifies the outbox writer
// is a no-op when the invoice is in manual_review status. No SELECT, no
// INSERT — the hold extends to Stripe meter delivery same as everywhere else.
func TestEnqueueMeterEvents_ManualReviewIsHardHold(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, _ := db.BeginTx(context.Background(), nil)
	if err := EnqueueMeterEvents(context.Background(), tx, "inv-1", "tenant-1", "manual_review"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestEnqueueMeterEvents_LinesWithoutMeterAreSkipped verifies that lines
// whose pricing source has no meter destination (free_unmetered, tier
// without a tenant-tier meter, etc.) produce no INSERTs.
func TestEnqueueMeterEvents_LinesWithoutMeterAreSkipped(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH lines AS`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "meter", "pricing_source", "quantity",
			"period_start", "period_end", "stripe_meter_event_name",
		}).
			AddRow(uuid.New().String(), "self-edge-1", "delivered_minutes", "self_hosted", "1000", time.Now(), time.Now(), nil))
	tx, _ := db.BeginTx(context.Background(), nil)
	if err := EnqueueMeterEvents(context.Background(), tx, "inv-1", "tenant-1", "pending"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestEnqueueMeterEvents_ClusterMeteredEnqueues verifies the happy path: a
// marketplace cluster_metered line with a stripe_meter_event_name produces one
// outbox INSERT.
func TestEnqueueMeterEvents_ClusterMeteredEnqueues(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lineID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH lines AS`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "meter", "pricing_source", "quantity",
			"period_start", "period_end", "stripe_meter_event_name",
		}).
			AddRow(lineID.String(), "operator-eu-1", "delivered_minutes", "cluster_metered", "60000", periodStart, periodEnd, "meter.delivered_minutes"))
	mock.ExpectExec(`INSERT INTO purser\.stripe_meter_events_outbox`).
		WithArgs("tenant-1", "operator-eu-1", "delivered_minutes", "meter.delivered_minutes", "60000",
			periodStart, periodEnd, "inv-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := EnqueueMeterEvents(context.Background(), tx, "inv-1", "tenant-1", "pending"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestEnqueueMeterEvents_DistinctMetersDoNotCollapse(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lineA := uuid.New()
	lineB := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH lines AS`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "meter", "pricing_source", "quantity",
			"period_start", "period_end", "stripe_meter_event_name",
		}).
			AddRow(lineA.String(), "operator-eu-1", "delivered_minutes", "cluster_metered", "60000", periodStart, periodEnd, "meter.delivered_minutes").
			AddRow(lineB.String(), "operator-eu-1", "average_storage_gb", "cluster_metered", "25", periodStart, periodEnd, "meter.average_storage_gb"))
	mock.ExpectExec(`INSERT INTO purser\.stripe_meter_events_outbox`).
		WithArgs("tenant-1", "operator-eu-1", "delivered_minutes", "meter.delivered_minutes", "60000",
			periodStart, periodEnd, "inv-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.stripe_meter_events_outbox`).
		WithArgs("tenant-1", "operator-eu-1", "average_storage_gb", "meter.average_storage_gb", "25",
			periodStart, periodEnd, "inv-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := EnqueueMeterEvents(context.Background(), tx, "inv-1", "tenant-1", "pending"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestEnqueueMeterEvents_TenantScopedLineNullClusterHandled covers the NULL
// cluster_id path for tenant-scoped lines (base_subscription). The COALESCE
// in the query lets the empty string round-trip into Go without aborting
// finalization. Tenant-scoped lines have no Stripe meter destination so
// they're skipped, but the SELECT must succeed.
func TestEnqueueMeterEvents_TenantScopedLineNullClusterHandled(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	lineID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(`WITH lines AS`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "meter", "pricing_source", "quantity",
			"period_start", "period_end", "stripe_meter_event_name",
		}).
			AddRow(lineID.String(), "", "", "tier", "1", periodStart, periodEnd, nil))

	tx, _ := db.BeginTx(context.Background(), nil)
	if err := EnqueueMeterEvents(context.Background(), tx, "inv-1", "tenant-1", "pending"); err != nil {
		t.Fatalf("EnqueueMeterEvents: %v (NULL cluster_id must not abort finalization)", err)
	}
	mock.ExpectCommit()
	_ = tx.Commit()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
