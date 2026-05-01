package handlers

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
)

// TestPersistInvoiceLineItems_UpsertsAllLines verifies the upsert SQL fires
// once per line in the rating result and once for the orphan-sweep query.
func TestPersistInvoiceLineItems_UpsertsAllLines(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	res := &clusterRatingResult{
		BaseLine: pricedLine{
			LineItem: rating.LineItem{
				LineKey: rating.LineKeyBaseSubscription, Description: "Base",
				Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1),
				UnitPrice: decimal.NewFromInt(79), Amount: decimal.NewFromInt(79),
				Currency: "EUR",
			},
			PricingSource: pricing.SourceTier,
		},
		UsageLines: []pricedLine{
			{
				LineItem: rating.LineItem{
					LineKey: "meter:delivered_minutes", Meter: rating.MeterDeliveredMinutes,
					Description: "Delivered minutes",
					Quantity:    decimal.NewFromInt(120000), IncludedQuantity: decimal.NewFromInt(100000),
					BillableQuantity: decimal.NewFromInt(20000), UnitPrice: decimal.NewFromFloat(0.00055),
					Amount: decimal.NewFromFloat(11.0), Currency: "EUR",
				},
				PricingSource: pricing.SourceTier,
			},
		},
	}

	// Expect one upsert per line (base + 1 usage line).
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Orphan sweep returns no extra rows.
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}))

	if err := persistInvoiceLineItems(context.Background(), mockDB, "inv-1", "tenant-1", res); err != nil {
		t.Fatalf("persistInvoiceLineItems: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestPersistInvoiceLineItems_SweepsStaleLines verifies that a line_key present
// in the DB but absent from the rating result is deleted, so re-rating after a
// meter is dropped from a tier doesn't leave ghost lines on the invoice.
func TestPersistInvoiceLineItems_SweepsStaleLines(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	res := &clusterRatingResult{
		BaseLine: pricedLine{
			LineItem: rating.LineItem{
				LineKey: rating.LineKeyBaseSubscription, Description: "Base",
				Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1),
				UnitPrice: decimal.NewFromInt(79), Amount: decimal.NewFromInt(79),
				Currency: "EUR",
			},
			PricingSource: pricing.SourceTier,
		},
	}

	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}).
			AddRow(rating.LineKeyBaseSubscription).
			AddRow("meter:ai_gpu_hours")) // stale row from a prior run
	mock.ExpectExec(`DELETE FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2 AND line_key = \$3`).
		WithArgs("inv-1", "tenant-1", "meter:ai_gpu_hours").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := persistInvoiceLineItems(context.Background(), mockDB, "inv-1", "tenant-1", res); err != nil {
		t.Fatalf("persistInvoiceLineItems: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

// TestWithTx_RollbackOnError verifies that withTx rolls the outer transaction
// back when fn returns an error, so partial writes never reach the DB.
func TestWithTx_RollbackOnError(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO purser.billing_invoices").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectRollback()

	wantErr := errors.New("synthetic line-item failure")
	gotErr := withTx(context.Background(), mockDB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `INSERT INTO purser.billing_invoices ...`); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("withTx err = %v, want it to wrap %v", gotErr, wantErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}
