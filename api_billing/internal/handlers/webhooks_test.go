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

	s := &Service{db: mockDB, logger: logrus.New()}

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

	ok, msg, code := s.ProcessStripeWebhookGRPC(body, headers)
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

func TestHandleStripeSubscriptionEventBackfillsBillingPeriod(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	tenantID := "11111111-1111-1111-1111-111111111111"
	subscriptionID := "sub_test_123"
	periodStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	payload := StripeWebhookPayload{
		ID:   "evt_sub_123",
		Type: "customer.subscription.updated",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(fmt.Sprintf(`{
				"id":"%s",
				"customer":"cus_test",
				"status":"active",
				"cancel_at_period_end":false,
				"items":{"data":[{"id":"si_test","current_period_start":%d,"current_period_end":%d}]}
			}`, subscriptionID, periodStart.Unix(), periodEnd.Unix())),
		},
	}

	mock.ExpectQuery(`SELECT tenant_id FROM purser\.tenant_subscriptions WHERE stripe_subscription_id = \$1`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenantID))
	// active routes through the activation helper: applies the tier, sets
	// payment_method=stripe, clears staged state, and backfills the period.
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions[\s\S]*payment_method = 'stripe'[\s\S]*billing_period_start = COALESCE\(\$6, billing_period_start\)[\s\S]*WHERE tenant_id = \$4`).
		WithArgs("cus_test", subscriptionID, "", tenantID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents[\s\S]*provider_subscription_id = \$1`).
		WithArgs(subscriptionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id FROM purser\.tenant_subscriptions WHERE tenant_id = \$1`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-local-1"))
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(eventSubscriptionUpdated, tenantID, "", "subscription", "sub-local-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := s.handleStripeSubscriptionEvent(payload); err != nil {
		t.Fatalf("handleStripeSubscriptionEvent: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeCheckoutAsyncPaymentFailedPrepaid asserts a failed delayed
// top-up payment moves the pending top-up to terminal 'failed' without
// crediting the balance.
func TestHandleStripeCheckoutAsyncPaymentFailedPrepaid(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	payload := StripeWebhookPayload{
		ID:   "evt_async_failed",
		Type: "checkout.session.async_payment_failed",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(`{"id":"cs_1","metadata":{"purpose":"prepaid","reference_id":"topup-1"}}`),
		},
	}

	mock.ExpectExec(`UPDATE purser\.pending_topups\s+SET status = \$1.*WHERE id = \$2 AND status = 'pending'`).
		WithArgs("failed", "topup-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := s.handleStripeCheckoutAsyncPaymentFailed(payload); err != nil {
		t.Fatalf("handleStripeCheckoutAsyncPaymentFailed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeCheckoutExpiredSubscription asserts an expired subscription
// checkout expires the open intent and clears the staged pending tier state so
// an abandoned upgrade does not strand the tenant.
func TestHandleStripeCheckoutExpiredSubscription(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	payload := StripeWebhookPayload{
		ID:   "evt_expired",
		Type: "checkout.session.expired",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(`{"id":"cs_1","subscription":"sub_1","metadata":{"purpose":"subscription","tenant_id":"t1"}}`),
		},
	}

	// expireStripeCheckoutIntent(sess.ID)
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET status = 'expired'.*provider_session_id = \$1`).
		WithArgs("cs_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// clearStagedStripeCheckout: expire intent by subscription, then clear tier state
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET status = 'expired'.*provider_subscription_id = \$1`).
		WithArgs("sub_1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = NULL.*WHERE tenant_id = \$1 AND pending_reason = 'stripe_checkout'`).
		WithArgs("t1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := s.handleStripeCheckoutExpired(payload); err != nil {
		t.Fatalf("handleStripeCheckoutExpired: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// TestHandleStripeCheckoutExpiredCluster asserts an expired cluster checkout
// expires the open intent and cancels the staged pending_payment cluster row so
// stale local state is not left behind.
func TestHandleStripeCheckoutExpiredCluster(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	payload := StripeWebhookPayload{
		ID:   "evt_expired_cluster",
		Type: "checkout.session.expired",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(`{"id":"cs_2","subscription":"sub_2","metadata":{"purpose":"cluster_subscription"}}`),
		},
	}

	mock.ExpectExec(`UPDATE purser\.payment_provider_intents\s+SET status = 'expired'.*provider_session_id = \$1`).
		WithArgs("cs_2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.cluster_subscriptions\s+SET status = 'cancelled'.*WHERE status = 'pending_payment'`).
		WithArgs("cs_2", "sub_2").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := s.handleStripeCheckoutExpired(payload); err != nil {
		t.Fatalf("handleStripeCheckoutExpired: %v", err)
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

	s := &Service{db: mockDB, logger: logrus.New()}

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

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_partial", "invoice-1", "confirmed")
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

	s := &Service{db: mockDB, logger: logrus.New()}

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
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}))
	mock.ExpectQuery(`WITH storage_lines`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "storage_provider_tenant_id", "storage_provider_cluster_id", "storage_backend",
			"usage_type", "currency", "period_start", "period_end", "allocated_gross_cents",
		}))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT bi\.tenant_id, bi\.amount, bi\.currency, ts\.billing_email`).
		WithArgs("invoice-2").
		WillReturnError(sql.ErrNoRows)

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_full", "invoice-2", "confirmed")
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

	s := &Service{db: mockDB, logger: logrus.New()}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, invoice_id FROM purser\.billing_payments`).
		WithArgs("tr_wrong", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id"}).AddRow("payment-3", "invoice-real"))
	mock.ExpectRollback()

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_wrong", "invoice-webhook", "confirmed")
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

	s := &Service{db: mockDB, logger: logrus.New()}

	mock.ExpectQuery(`SELECT COALESCE\(SUM\(amount_cents\), 0\)`).
		WithArgs("refund", "mollie-refund:tr_123:%").
		WillReturnRows(sqlmock.NewRows([]string{"already_applied"}).AddRow(int64(500)))

	delta, err := s.mollieReversalDelta(context.Background(), "refund", "tr_123", 1200)
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
	s := &Service{logger: logrus.New()}
	t.Setenv("STRIPE_WEBHOOK_SECRET", "")

	body := []byte(`{"id":"evt_missing_secret"}`)
	headers := map[string]string{
		"Stripe-Signature": "t=123,v1=deadbeef",
	}

	ok, msg, code := s.ProcessStripeWebhookGRPC(body, headers)
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 503 {
		t.Fatalf("expected 503, got %d (msg=%q)", code, msg)
	}
}

func TestProcessStripeWebhookGRPCInvalidSignature(t *testing.T) {
	s := &Service{logger: logrus.New()}
	t.Setenv("STRIPE_WEBHOOK_SECRET", "unit-test-secret")

	body := []byte(`{"id":"evt_invalid_signature"}`)
	headers := map[string]string{
		"Stripe-Signature": "t=123,v1=deadbeef",
	}

	ok, msg, code := s.ProcessStripeWebhookGRPC(body, headers)
	if ok {
		t.Fatalf("expected ok=false, got true (msg=%q)", msg)
	}
	if code != 401 {
		t.Fatalf("expected 401, got %d (msg=%q)", code, msg)
	}
}

func TestProcessStripeWebhookGRPCInvalidPayload(t *testing.T) {
	s := &Service{logger: logrus.New()}
	t.Setenv("STRIPE_WEBHOOK_SECRET", "unit-test-secret")

	body := []byte(`not-json`)
	signature := stripeSignatureHeader(body, "unit-test-secret", time.Now().Unix())
	headers := map[string]string{
		"Stripe-Signature": signature,
	}

	ok, msg, code := s.ProcessStripeWebhookGRPC(body, headers)
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
