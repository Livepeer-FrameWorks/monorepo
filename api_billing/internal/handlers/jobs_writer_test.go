package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/pkg/billing"
	"frameworks/pkg/logging"
)

func TestCollectInvoiceUsageAggregatesRows(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	// Rows now carry cluster_id; legacy unattributed rows arrive as "".
	// Two distinct clusters split the same meter to verify partitioning.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "average_storage_gb", 2.5).
			AddRow("cluster-a", "viewer_hours", 3.0).
			AddRow("cluster-b", "viewer_hours", 1.5))

	got, err := jm.collectInvoiceUsage(context.Background(), "tenant-1", start, end)
	if err != nil {
		t.Fatalf("collectInvoiceUsage: %v", err)
	}
	if got[""]["average_storage_gb"] != 2.5 {
		t.Errorf("legacy bucket missing average_storage_gb: %v", got[""])
	}
	if got["cluster-a"]["viewer_hours"] != 3.0 {
		t.Errorf("cluster-a viewer_hours = %v, want 3.0", got["cluster-a"]["viewer_hours"])
	}
	if got["cluster-b"]["viewer_hours"] != 1.5 {
		t.Errorf("cluster-b viewer_hours = %v, want 1.5", got["cluster-b"]["viewer_hours"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestCollectInvoiceUsageRowsErrorFailsClosed(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "viewer_hours", 3.0).
			RowError(0, errors.New("cursor failed")))

	_, err = jm.collectInvoiceUsage(context.Background(), "tenant-1", start, end)
	if err == nil {
		t.Fatalf("collectInvoiceUsage err = nil, want cursor failure")
	}
	if !strings.Contains(err.Error(), "usage row") && !strings.Contains(err.Error(), "usage rows") {
		t.Fatalf("collectInvoiceUsage err = %v, want usage row context", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestUpdateInvoiceDraftWritesRatedLineItemsTransactionally(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-1"
	tierID := "tier-1"
	subscriptionID := "sub-1"
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	currency := billing.DefaultCurrency()

	mock.ExpectQuery(`SELECT bt\.id, bt\.tier_name, bt\.base_price::text`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).
			AddRow(tierID, "supporter", "100.00", currency, true, subscriptionID))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("average_storage_gb", "all_usage", currency, "0", "1.00", "{}"))
	mock.ExpectQuery(`FROM purser\.subscription_pricing_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`FROM purser\.tier_entitlements`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery(`FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	mock.ExpectQuery(`SELECT billing_period_start, billing_period_end`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end"}).
			AddRow(periodStart, periodEnd))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM purser\.billing_invoices`).
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// New per-cluster shape: rows now carry cluster_id. Empty cluster_id
	// keeps the legacy platform-official path.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "average_storage_gb", 2.0))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT amount_cents FROM purser\.balance_transactions`).
		WithArgs(tenantID, sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(0)))
	mock.ExpectQuery(`INSERT INTO purser\.billing_invoices`).
		WithArgs(tenantID, "102", currency, sqlmock.AnyArg(), "100", "2", "0", sqlmock.AnyArg(), periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("invoice-1"))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}).
			AddRow("base_subscription").
			AddRow("meter:average_storage_gb"))
	mock.ExpectCommit()

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestUpdateInvoiceDraftClampsPriorPrepaidCreditToZeroNet(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-1"
	tierID := "tier-1"
	subscriptionID := "sub-1"
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	currency := billing.DefaultCurrency()

	mock.ExpectQuery(`SELECT bt\.id, bt\.tier_name, bt\.base_price::text`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).
			AddRow(tierID, "supporter", "100.00", currency, true, subscriptionID))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("average_storage_gb", "all_usage", currency, "0", "1.00", "{}"))
	mock.ExpectQuery(`FROM purser\.subscription_pricing_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`FROM purser\.tier_entitlements`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery(`FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	mock.ExpectQuery(`SELECT billing_period_start, billing_period_end`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end"}).
			AddRow(periodStart, periodEnd))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM purser\.billing_invoices`).
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "average_storage_gb", 2.0))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT amount_cents FROM purser\.balance_transactions`).
		WithArgs(tenantID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"amount_cents"}).AddRow(int64(-20_000)))
	mock.ExpectQuery(`INSERT INTO purser\.billing_invoices`).
		WithArgs(tenantID, "0", currency, sqlmock.AnyArg(), "100", "2", "200", sqlmock.AnyArg(), periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("invoice-1"))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}).
			AddRow("base_subscription").
			AddRow("meter:average_storage_gb"))
	mock.ExpectCommit()

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
