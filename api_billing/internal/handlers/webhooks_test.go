package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestProcessStripeWebhookGRPCIdempotent(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	metrics = nil
	t.Cleanup(func() {
		db = nil
	})

	t.Setenv("STRIPE_WEBHOOK_SECRET", "unit-test-secret")

	payload := StripeWebhookPayload{
		ID:   "evt_test_123",
		Type: "payment_intent.succeeded",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(`{"id":"pi_test"}`),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	signature := stripeSignatureHeader(body, "unit-test-secret", time.Now().Unix())
	headers := map[string]string{
		"Stripe-Signature": signature,
	}

	mock.ExpectQuery("SELECT EXISTS\\(SELECT 1 FROM purser.webhook_events").
		WithArgs("stripe", "evt_test_123").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	ok, msg, code := ProcessStripeWebhookGRPC(body, headers)
	if !ok {
		t.Fatalf("expected ok=true, got false (msg=%q)", msg)
	}
	if code != 200 {
		t.Fatalf("expected 200, got %d (msg=%q)", code, msg)
	}
	if msg != "" {
		t.Fatalf("expected empty message, got %q", msg)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateInvoicePaymentStatusDoesNotMarkPartiallyPaidInvoicePaid(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() { db = nil })

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, invoice_id FROM purser\.billing_payments`).
		WithArgs("tr_partial", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id"}).AddRow("payment-1", "invoice-1"))
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("confirmed", sqlmock.AnyArg(), "payment-1", "tr_partial").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices[\s\S]*COALESCE\(SUM\(amount\), 0\)[\s\S]*>= amount`).
		WithArgs(sqlmock.AnyArg(), "invoice-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT bi\.tenant_id, bi\.amount, bi\.currency, ts\.billing_email`).
		WithArgs("invoice-1").
		WillReturnError(sql.ErrNoRows)

	updated, err := updateInvoicePaymentStatus("mollie", "tr_partial", "invoice-1", "confirmed")
	if err != nil {
		t.Fatalf("updateInvoicePaymentStatus: %v", err)
	}
	if !updated {
		t.Fatal("expected payment row to update")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateInvoicePaymentStatusMarksInvoicePaidWhenConfirmedPaymentsCoverAmount(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() { db = nil })

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, invoice_id FROM purser\.billing_payments`).
		WithArgs("tr_full", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id"}).AddRow("payment-2", "invoice-2"))
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("confirmed", sqlmock.AnyArg(), "payment-2", "tr_full").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices[\s\S]*COALESCE\(SUM\(amount\), 0\)[\s\S]*>= amount`).
		WithArgs(sqlmock.AnyArg(), "invoice-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT bi\.tenant_id, bi\.amount, bi\.currency, ts\.billing_email`).
		WithArgs("invoice-2").
		WillReturnError(sql.ErrNoRows)

	updated, err := updateInvoicePaymentStatus("mollie", "tr_full", "invoice-2", "confirmed")
	if err != nil {
		t.Fatalf("updateInvoicePaymentStatus: %v", err)
	}
	if !updated {
		t.Fatal("expected payment row to update")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestProcessStripeWebhookGRPCMissingSecret(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")

	body := []byte(`{"id":"evt_missing_secret"}`)
	headers := map[string]string{
		"Stripe-Signature": "t=123,v1=deadbeef",
	}

	ok, msg, code := ProcessStripeWebhookGRPC(body, headers)
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 503 {
		t.Fatalf("expected 503, got %d (msg=%q)", code, msg)
	}
}

func TestProcessStripeWebhookGRPCInvalidSignature(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", "unit-test-secret")

	body := []byte(`{"id":"evt_invalid_signature"}`)
	headers := map[string]string{
		"Stripe-Signature": "t=123,v1=deadbeef",
	}

	ok, msg, code := ProcessStripeWebhookGRPC(body, headers)
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 401 {
		t.Fatalf("expected 401, got %d (msg=%q)", code, msg)
	}
}

func TestProcessStripeWebhookGRPCInvalidPayload(t *testing.T) {
	t.Setenv("STRIPE_WEBHOOK_SECRET", "unit-test-secret")

	body := []byte(`not-json`)
	signature := stripeSignatureHeader(body, "unit-test-secret", time.Now().Unix())
	headers := map[string]string{
		"Stripe-Signature": signature,
	}

	ok, msg, code := ProcessStripeWebhookGRPC(body, headers)
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 400 {
		t.Fatalf("expected 400, got %d (msg=%q)", code, msg)
	}
}

func stripeSignatureHeader(payload []byte, secret string, timestamp int64) string {
	signedPayload := fmt.Sprintf("%d.%s", timestamp, payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	expectedSignature := hex.EncodeToString(mac.Sum(nil))
	return fmt.Sprintf("t=%d,v1=%s", timestamp, expectedSignature)
}
