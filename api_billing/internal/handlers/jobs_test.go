package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"

	"frameworks/pkg/logging"
)

func TestUpdateInvoiceDraftSkipsWhenFinalizedInvoiceExists(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	jm := &JobManager{
		db:     db,
		logger: logging.NewLogger(),
	}

	tenantID := "tenant-1"
	periodStart := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT ts.tier_id").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"tier_id",
			"status",
			"tier_name",
			"display_name",
			"base_price",
			"currency",
			"metering_enabled",
			"overage_rates",
			"storage_allocation",
			"bandwidth_allocation",
			"custom_pricing",
			"custom_allocations",
		}).AddRow(
			"tier-basic",
			"active",
			"basic",
			"Basic",
			100.0,
			"EUR",
			false,
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
		))

	mock.ExpectQuery("SELECT billing_period_start, billing_period_end").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end"}).
			AddRow(periodStart, periodEnd))

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM purser.billing_invoices").
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestUpdateInvoiceDraftAppliesPrepaidCredit(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	jm := &JobManager{
		db:     db,
		logger: logging.NewLogger(),
	}

	tenantID := "tenant-credit"
	periodStart := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC)
	referenceID := uuid.NewSHA1(
		uuid.NameSpaceOID,
		[]byte(fmt.Sprintf("invoice_credit:%s:%s", tenantID, periodStart.Format("2006-01-02"))),
	).String()

	mock.ExpectQuery("SELECT ts.tier_id").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{
			"tier_id",
			"status",
			"tier_name",
			"display_name",
			"base_price",
			"currency",
			"metering_enabled",
			"overage_rates",
			"storage_allocation",
			"bandwidth_allocation",
			"custom_pricing",
			"custom_allocations",
		}).AddRow(
			"tier-basic",
			"active",
			"basic",
			"Basic",
			100.0,
			"EUR",
			false,
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
		))

	mock.ExpectQuery("SELECT billing_period_start, billing_period_end").
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end"}).
			AddRow(periodStart, periodEnd))

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM purser.billing_invoices").
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectQuery("FROM purser.usage_records").
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"usage_type", "total"}))

	mock.ExpectQuery("SELECT balance_cents FROM purser.prepaid_balances").
		WithArgs(tenantID, "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(15000)))

	mock.ExpectQuery("SELECT amount_cents FROM purser.balance_transactions").
		WithArgs(tenantID, "invoice_credit", referenceID).
		WillReturnError(sql.ErrNoRows)

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO purser.prepaid_balances").
		WithArgs(tenantID, "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT balance_cents").
		WithArgs(tenantID, "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(15000)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(5000), tenantID, "EUR").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO purser.balance_transactions").
		WithArgs(tenantID, int64(-10000), int64(5000), sqlmock.AnyArg(), sqlmock.AnyArg(), "invoice_credit").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	mock.ExpectExec("UPDATE purser.billing_invoices").
		WithArgs(
			0.0,
			100.0,
			0.0,
			100.0,
			"EUR",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			periodStart,
			periodEnd,
			tenantID,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft returned error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestGenerateMonthlyInvoicesUpdatesDraftWithOverlappingUsage(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	jm := &JobManager{
		db:     db,
		logger: logging.NewLogger(),
	}

	now := time.Now().UTC()
	periodEnd := now.Add(-time.Hour)
	periodStart := periodEnd.AddDate(0, -1, 0)

	mock.ExpectQuery("SELECT ts.tenant_id, ts.billing_email, ts.tier_id").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"tenant_id",
			"billing_email",
			"tier_id",
			"status",
			"billing_period_start",
			"billing_period_end",
			"tier_name",
			"display_name",
			"base_price",
			"currency",
			"billing_period",
			"metering_enabled",
			"overage_rates",
			"storage_allocation",
			"bandwidth_allocation",
			"custom_pricing",
			"custom_features",
			"custom_allocations",
		}).AddRow(
			"tenant-overlap",
			"",
			"tier-basic",
			"active",
			periodStart,
			periodEnd,
			"basic",
			"Basic",
			100.0,
			"EUR",
			"monthly",
			false,
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
			[]byte(`{}`),
		))

	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM purser.billing_invoices").
		WithArgs("tenant-overlap", periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	mock.ExpectQuery("SELECT id FROM purser.billing_invoices").
		WithArgs("tenant-overlap", periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("draft-1"))

	mock.ExpectQuery("period_start < \\$3[\\s\\S]*period_end > \\$2").
		WithArgs("tenant-overlap", periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"usage_type", "aggregated_value"}).
			AddRow("viewer_hours", 10.0))

	mock.ExpectExec("UPDATE purser.billing_invoices").
		WithArgs(
			100.0,
			100.0,
			0.0,
			"EUR",
			"pending",
			sqlmock.AnyArg(),
			sqlmock.AnyArg(),
			periodStart,
			periodEnd,
			"draft-1",
		).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("UPDATE purser.tenant_subscriptions").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "tenant-overlap").
		WillReturnResult(sqlmock.NewResult(1, 1))

	jm.generateMonthlyInvoices()

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
