package handlers

import (
	"encoding/json"
	"testing"

	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestStripeRecoveryRequiresTenantScopedReviewReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("-- name: HasBillingTaxReviewReceipt").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	jm := &JobManager{db: db}
	if err = jm.recordStripeInvoiceTaxReview(t.Context(), "tenant-a", billingstripe.TaxInvoiceReview{InvoiceID: "in_collision", Code: "review", InputFingerprint: "fingerprint", Evidence: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("recovery acknowledged a review with no tenant-scoped durable receipt")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStripeRecoveryAcceptsUnchangedAcknowledgedReviewReceipt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectExec("-- name: RecordBillingTaxReview").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("-- name: HasBillingTaxReviewReceipt").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	jm := &JobManager{db: db}
	if err = jm.recordStripeInvoiceTaxReview(t.Context(), "tenant-a", billingstripe.TaxInvoiceReview{InvoiceID: "in_acknowledged", Code: "review", InputFingerprint: "fingerprint", Evidence: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
