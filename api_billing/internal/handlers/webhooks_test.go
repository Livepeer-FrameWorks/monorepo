package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mollie "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/sirupsen/logrus"
)

func TestProcessStripeWebhookGRPCIdempotent(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
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

	// Claim/lock returns the row in its existing terminal state, so the
	// caller short-circuits without re-processing or marking again.
	mock.ExpectQuery(`INSERT INTO purser\.webhook_events`).
		WithArgs("stripe", "evt_test_123", "payment_intent.succeeded", signature, sqlmock.AnyArg(), int(webhookClaimLease/time.Second)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "acquired"}).AddRow("processed", false))

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

func TestMollieEventIDForPaymentIncludesCumulativeReversalAmounts(t *testing.T) {
	payment := &mollie.Payment{
		ID:     "tr_123",
		Status: "paid",
		AmountRefunded: &mollie.Amount{
			Value:    "12.00",
			Currency: "eur",
		},
		AmountChargedBack: &mollie.Amount{
			Value:    "3.50",
			Currency: "usd",
		},
	}

	got := mollieEventIDForPayment(payment, strings.ToLower(payment.Status))
	want := "payment:tr_123:paid:refunded:12.00:EUR:charged_back:3.50:USD"
	if got != want {
		t.Fatalf("mollieEventIDForPayment() = %q, want %q", got, want)
	}
}

// TestMollieAmountToCents pins exact decimal parsing for Mollie amounts.
// float intermediates would round 0.10 + 0.20 to 0.30000000000000004 cents;
// the integer path is byte-exact across currencies with different exponents.
func TestMollieAmountToCentsExact(t *testing.T) {
	cases := []struct {
		value    string
		currency string
		want     int64
	}{
		{"9.95", "EUR", 995},
		{"0.01", "EUR", 1},
		{"100.00", "USD", 10000},
		{"0.10", "EUR", 10},
		{"123", "JPY", 123},
		{"4.500", "BHD", 4500},
	}
	for _, tc := range cases {
		got, _, err := mollieAmountToCents(tc.value, tc.currency)
		if err != nil {
			t.Fatalf("mollieAmountToCents(%q,%q) err: %v", tc.value, tc.currency, err)
		}
		if got != tc.want {
			t.Fatalf("mollieAmountToCents(%q,%q) = %d, want %d", tc.value, tc.currency, got, tc.want)
		}
	}

	if _, _, err := mollieAmountToCents("9.995", "EUR"); err == nil {
		t.Fatal("expected error for over-precise EUR amount")
	}
	if _, _, err := mollieAmountToCents("", "EUR"); err == nil {
		t.Fatal("expected error for empty amount")
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
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("succeeded", "tr_partial", "payment-1", "mollie").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.billing_invoices[\s\S]*COALESCE\(SUM[\s\S]*currency = i\.currency[\s\S]*>= i\.amount`).
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
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("succeeded", "tr_full", "payment-2", "mollie").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.billing_invoices[\s\S]*COALESCE\(SUM[\s\S]*currency = i\.currency[\s\S]*>= i\.amount`).
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

func TestUpdateInvoicePaymentStatusRejectsInvoiceMismatch(t *testing.T) {
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
		WithArgs("tr_wrong", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id"}).AddRow("payment-3", "invoice-real"))
	mock.ExpectRollback()

	updated, err := updateInvoicePaymentStatus("mollie", "tr_wrong", "invoice-webhook", "confirmed")
	if err == nil {
		t.Fatal("expected invoice mismatch error")
	}
	if updated {
		t.Fatal("expected no update on invoice mismatch")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestMollieReversalDeltaUsesCumulativeAmount(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	t.Cleanup(func() { db = nil })

	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount_cents\), 0\)`).
		WithArgs("refund", "mollie-refund:tr_123:%").
		WillReturnRows(sqlmock.NewRows([]string{"already_applied"}).AddRow(int64(500)))

	delta, err := mollieReversalDelta(context.Background(), "refund", "tr_123", 1200)
	if err != nil {
		t.Fatalf("mollieReversalDelta: %v", err)
	}
	if delta != 700 {
		t.Fatalf("delta = %d, want 700", delta)
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
