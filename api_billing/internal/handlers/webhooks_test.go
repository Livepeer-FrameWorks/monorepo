package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig/appconfigtest"
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

	appconfigtest.Set(t, "STRIPE_WEBHOOK_SECRET", "unit-test-secret")

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

	// The provider event id is the inbox idempotency key, so a duplicate
	// delivery is acknowledged without running reconciliation inline.
	mock.ExpectExec(`INSERT INTO purser\.provider_webhook_inbox`).
		WithArgs("stripe", "evt_test_123", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

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

	expectStripeSubscriptionActivation(mock, tenantID, subscriptionID)
	updated := expectDomainEvent(mock, "billing.subscription_updated", tenantID)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{updated}, eventSubscriptionUpdated, tenantID, "", "subscription", "sub-local-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := s.handleStripeSubscriptionEvent(payload); err != nil {
		t.Fatalf("handleStripeSubscriptionEvent: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

// The subscription_updated outbox row shares the activation transaction: a
// failed insert rolls back the tier activation and intent update and returns
// the error so the webhook is retried.
func TestHandleStripeSubscriptionEventRollsBackActivationWhenOutboxInsertFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	tenantID := "11111111-1111-1111-1111-111111111111"
	subscriptionID := "sub_test_rollback"
	payload := StripeWebhookPayload{
		ID:   "evt_sub_rollback",
		Type: "customer.subscription.updated",
		Data: struct {
			Object json.RawMessage `json:"object"`
		}{
			Object: json.RawMessage(fmt.Sprintf(`{
				"id":"%s",
				"customer":"cus_test",
				"status":"active",
				"cancel_at_period_end":false,
				"items":{"data":[{"id":"si_test","current_period_start":1780272000,"current_period_end":1782864000}]}
			}`, subscriptionID)),
		},
	}

	expectStripeSubscriptionActivation(mock, tenantID, subscriptionID)
	updated := expectDomainEvent(mock, "billing.subscription_updated", tenantID)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{updated}, eventSubscriptionUpdated, tenantID, "", "subscription", "sub-local-1", sqlmock.AnyArg()).
		WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()

	err = s.handleStripeSubscriptionEvent(payload)
	if err == nil || !strings.Contains(err.Error(), "outbox unavailable") {
		t.Fatalf("expected outbox insert error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectStripeSubscriptionActivation(mock sqlmock.Sqlmock, tenantID, subscriptionID string) {
	mock.ExpectQuery(`SELECT tenant_id::text AS tenant_id`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenantID))
	mock.ExpectBegin()
	// active routes through the activation helper: applies the tier, sets
	// payment_method=stripe, clears staged state, and backfills the period.
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions[\s\S]*payment_method = 'stripe'[\s\S]*billing_period_start = COALESCE\(\$5, billing_period_start\)[\s\S]*WHERE tenant_id = \$6::text::uuid`).
		WithArgs("cus_test", subscriptionID, "", sqlmock.AnyArg(), sqlmock.AnyArg(), tenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents[\s\S]*provider_subscription_id = \$1`).
		WithArgs(subscriptionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT id::text AS id`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("sub-local-1"))
}

func stripePaymentFailedPayload() StripeWebhookPayload {
	var payload StripeWebhookPayload
	payload.ID = "evt_pi_failed"
	payload.Type = "payment_intent.payment_failed"
	payload.Data.Object = json.RawMessage(`{"id":"pi_fail","currency":"eur","metadata":{"invoice_id":"invoice-1","tenant_id":"` + webhookTenantID + `"},` +
		`"last_payment_error":{"code":"card_declined","decline_code":"insufficient_funds"}}`)
	return payload
}

// expectStripePaymentFailedStatusUpdate expects the pending → failed update of
// payment-1 and the EUR amount read for its billing.payment_failed event, and
// returns the captured ID of that event.
func expectStripePaymentFailedStatusUpdate(mock sqlmock.Sqlmock) *domainEventID {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("pi_fail", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).
			AddRow("payment-1", "invoice-1", webhookTenantID, "12.50", "EUR", "pending"))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-1", webhookTenantID, "pending")
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("failed", sqlmock.AnyArg(), "pi_fail", "payment-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("failed", "pi_fail", "payment-1", "stripe").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`-- name: GetPaymentEventAmount`).
		WithArgs("payment-1", webhookTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"eur_amount_cents"}).AddRow(int64(1250)))
	return expectDomainEvent(mock, "billing.payment_failed", webhookTenantID)
}

// The payment_failed outbox row is written inside the payment status
// transaction, before commit; the provider-object mapping runs afterwards.
func TestHandleStripePaymentIntentWritesPaymentEventInStatusTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	failed := expectStripePaymentFailedStatusUpdate(mock)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{failed}, eventPaymentFailed, webhookTenantID, "", "payment", "payment-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT invoice\.tenant_id::text AS tenant_id,[\s\S]*FROM purser\.billing_payments payment`).
		WithArgs("payment-1", "invoice-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, invoice\.tenant_id::text AS tenant_id`).
		WithArgs("invoice-1", "pi_fail").
		WillReturnError(sql.ErrNoRows)

	if err := s.handleStripePaymentIntentGRPC(stripePaymentFailedPayload()); err != nil {
		t.Fatalf("handleStripePaymentIntentGRPC: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandleStripePaymentIntentRollsBackStatusWhenOutboxInsertFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	failed := expectStripePaymentFailedStatusUpdate(mock)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sameEventID{failed}, eventPaymentFailed, webhookTenantID, "", "payment", "payment-1", sqlmock.AnyArg()).
		WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()

	err = s.handleStripePaymentIntentGRPC(stripePaymentFailedPayload())
	if err == nil || !strings.Contains(err.Error(), "outbox unavailable") {
		t.Fatalf("expected outbox insert error, got %v", err)
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

	mock.ExpectExec(`UPDATE purser\.pending_topups\s+SET status = \$1.*WHERE id = \$2::text::uuid AND status = 'pending'`).
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
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET pending_tier_id = NULL.*WHERE tenant_id = \$1::text::uuid AND pending_reason = 'stripe_checkout'`).
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

// TestIntPow10 pins the integer 10^n divisor used to render minor units back to
// a major-unit float for invoice display (amountCents / 10^exponent). It must
// agree exactly with the currency minor-unit exponents, or displayed invoice
// amounts are off by orders of magnitude.
func TestIntPow10(t *testing.T) {
	cases := map[int]int64{0: 1, 1: 10, 2: 100, 3: 1000}
	for n, want := range cases {
		if got := intPow10(n); got != want {
			t.Fatalf("intPow10(%d) = %d, want %d", n, got, want)
		}
	}

	// The divisor must match the exponent each currency class declares.
	for _, currency := range []string{"JPY", "EUR", "USD", "BHD"} {
		exp := currencyMinorUnitExponent(currency)
		got := intPow10(exp)
		want := int64(1)
		for i := 0; i < exp; i++ {
			want *= 10
		}
		if got != want {
			t.Fatalf("intPow10(exponent of %s=%d) = %d, want %d", currency, exp, got, want)
		}
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
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("tr_partial", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).AddRow("payment-1", "invoice-1", "tenant-1", "10.00", "EUR", "pending"))
	// Settlement reads an aggregate over sibling payments, so it serializes on
	// the invoice first -- the same lock the payment-creation path takes.
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-1", "tenant-1", "pending")
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("confirmed", sqlmock.AnyArg(), "tr_partial", "payment-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("succeeded", "tr_partial", "payment-1", "mollie").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.provider_settlements`).
		WithArgs("tenant-1", "mollie", "tr_partial", nil, "payment-1", int64(1000), "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices invoice[\s\S]*COALESCE\(SUM[\s\S]*invoice\.presentment_currency[\s\S]*>= COALESCE\(invoice\.presentment_amount_cents`).
		WithArgs(sqlmock.AnyArg(), "invoice-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT invoice\.tenant_id::text AS tenant_id,[\s\S]*FROM purser\.billing_payments payment`).
		WithArgs("payment-1", "invoice-1").
		WillReturnError(sql.ErrNoRows)

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_partial", "invoice-1", "confirmed", nil, providerSettlementEvidence{
		TenantID: "tenant-1", AmountCents: 1000, Currency: "EUR",
	})
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

// The Mollie payment event is written by updateInvoicePaymentStatus inside the
// status transaction, before commit.
func TestUpdateInvoicePaymentStatusWritesMolliePaymentEventInTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}
	settlement := providerSettlementEvidence{TenantID: "tenant-1", AmountCents: 1000, Currency: "EUR"}

	expectMolliePartialPaymentConfirmation(mock)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventPaymentSucceeded, "tenant-1", "", "payment", "tr_partial", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT invoice\.tenant_id::text AS tenant_id,[\s\S]*FROM purser\.billing_payments payment`).
		WithArgs("payment-1", "invoice-1").
		WillReturnError(sql.ErrNoRows)

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_partial", "invoice-1", "confirmed",
		molliePaymentEventWriter("tr_partial", "confirmed", settlement), settlement)
	if err != nil || !updated {
		t.Fatalf("updateInvoicePaymentStatus = updated %v, error %v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateInvoicePaymentStatusRollsBackWhenMolliePaymentEventFails(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}
	settlement := providerSettlementEvidence{TenantID: "tenant-1", AmountCents: 1000, Currency: "EUR"}

	expectMolliePartialPaymentConfirmation(mock)
	mock.ExpectExec(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventPaymentSucceeded, "tenant-1", "", "payment", "tr_partial", sqlmock.AnyArg()).
		WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_partial", "invoice-1", "confirmed",
		molliePaymentEventWriter("tr_partial", "confirmed", settlement), settlement)
	if err == nil || !strings.Contains(err.Error(), "outbox unavailable") || updated {
		t.Fatalf("updateInvoicePaymentStatus = updated %v, error %v; want outbox error", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectMolliePartialPaymentConfirmation(mock sqlmock.Sqlmock) {
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("tr_partial", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).AddRow("payment-1", "invoice-1", "tenant-1", "10.00", "EUR", "pending"))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-1", "tenant-1", "pending")
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("confirmed", sqlmock.AnyArg(), "tr_partial", "payment-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("succeeded", "tr_partial", "payment-1", "mollie").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.provider_settlements`).
		WithArgs("tenant-1", "mollie", "tr_partial", nil, "payment-1", int64(1000), "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices invoice[\s\S]*COALESCE\(SUM[\s\S]*invoice\.presentment_currency[\s\S]*>= COALESCE\(invoice\.presentment_amount_cents`).
		WithArgs(sqlmock.AnyArg(), "invoice-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func TestUpdateInvoicePaymentStatusMarksInvoicePaidWhenConfirmedPaymentsCoverAmount(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("tr_full", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).AddRow("payment-2", "invoice-2", webhookTenantID, "25.00", "EUR", "pending"))
	// Settlement reads an aggregate over sibling payments, so it serializes on
	// the invoice first -- the same lock the payment-creation path takes.
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-2").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-2", webhookTenantID, "pending")
	mock.ExpectExec(`UPDATE purser\.billing_payments`).
		WithArgs("confirmed", sqlmock.AnyArg(), "tr_full", "payment-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("succeeded", "tr_full", "payment-2", "mollie").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO purser\.provider_settlements`).
		WithArgs(webhookTenantID, "mollie", "tr_full", nil, "payment-2", int64(2500), "EUR").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_invoices invoice[\s\S]*COALESCE\(SUM[\s\S]*invoice\.presentment_currency[\s\S]*>= COALESCE\(invoice\.presentment_amount_cents`).
		WithArgs(sqlmock.AnyArg(), "invoice-2").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM purser\.invoice_line_items li`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "cluster_id", "cluster_owner_tenant_id", "operator_credit_cents", "platform_fee_cents", "currency", "period_start", "period_end",
		}))
	mock.ExpectQuery(`WITH provider_lines`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "storage_provider_tenant_id", "storage_provider_cluster_id", "storage_backend",
			"usage_type", "currency", "period_start", "period_end", "allocated_gross_cents",
		}))
	mock.ExpectQuery(`-- name: GetInvoiceEventState`).
		WithArgs("invoice-2").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "amount"}).AddRow(webhookTenantID, "25.00"))
	expectDomainEvent(mock, "billing.invoice_paid", webhookTenantID)
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT invoice\.tenant_id::text AS tenant_id,[\s\S]*FROM purser\.billing_payments payment`).
		WithArgs("payment-2", "invoice-2").
		WillReturnError(sql.ErrNoRows)

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_full", "invoice-2", "confirmed", nil, providerSettlementEvidence{
		TenantID: webhookTenantID, AmountCents: 2500, Currency: "EUR",
	})
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
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("tr_wrong", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).AddRow("payment-3", "invoice-real", "tenant-3", "10.00", "EUR", "pending"))
	mock.ExpectRollback()

	updated, err := s.updateInvoicePaymentStatus("mollie", "tr_wrong", "invoice-webhook", "confirmed", nil, providerSettlementEvidence{
		TenantID: "tenant-3", AmountCents: 1000, Currency: "EUR",
	})
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

func TestUpdateInvoicePaymentStatusRejectsSettlementEvidenceMismatch(t *testing.T) {
	tests := []struct {
		name     string
		evidence providerSettlementEvidence
	}{
		{name: "tenant", evidence: providerSettlementEvidence{TenantID: "tenant-forged", AmountCents: 1250, Currency: "EUR"}},
		{name: "underpayment", evidence: providerSettlementEvidence{TenantID: "tenant-real", AmountCents: 1249, Currency: "EUR"}},
		{name: "overpayment", evidence: providerSettlementEvidence{TenantID: "tenant-real", AmountCents: 1251, Currency: "EUR"}},
		{name: "currency", evidence: providerSettlementEvidence{TenantID: "tenant-real", AmountCents: 1250, Currency: "USD"}},
		{name: "missing amount", evidence: providerSettlementEvidence{TenantID: "tenant-real", Currency: "EUR"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("failed to create sqlmock: %v", err)
			}
			defer mockDB.Close()

			s := &Service{db: mockDB, logger: logrus.New()}
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
				WithArgs("pi_settlement", "card").
				WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).
					AddRow("payment-1", "invoice-1", "tenant-real", "12.50", "EUR", "pending"))
			if tc.name != "tenant" {
				mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
				expectLockedPaymentStatus(mock, "payment-1", "tenant-real", "pending")
			}
			mock.ExpectRollback()

			updated, err := s.updateInvoicePaymentStatus("stripe", "pi_settlement", "invoice-1", "confirmed", nil, tc.evidence)
			if err == nil {
				t.Fatal("expected settlement evidence mismatch")
			}
			if updated {
				t.Fatal("expected no payment mutation")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet expectations: %v", err)
			}
		})
	}
}

// A redelivered webhook for an already-confirmed payment no longer returns
// early. It re-runs the settlement recompute, because that replay is the only
// event that recurs for a settled invoice and so the only thing that can rescue
// one whose settlement was lost. The recompute stays harmless on the ordinary
// path: the UPDATE only matches an invoice still pending or overdue, and
// operator credits are only written when it changes a row.
func TestUpdateInvoicePaymentStatusConfirmedReplayRecomputesSettlement(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("pi_replay", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).
			AddRow("payment-1", "invoice-1", "tenant-1", "12.50", "EUR", "confirmed"))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-1", "tenant-1", "confirmed")
	// Already covered by an earlier settlement: the recompute matches no row and
	// writes nothing, so no operator-credit work follows.
	mock.ExpectExec(`UPDATE purser\.billing_invoices`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	updated, err := s.updateInvoicePaymentStatus("stripe", "pi_replay", "invoice-1", "confirmed", nil, providerSettlementEvidence{
		TenantID: "tenant-1", AmountCents: 1250, Currency: "EUR",
	})
	if err != nil || !updated {
		t.Fatalf("confirmed replay = updated %v, error %v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestUpdateInvoicePaymentStatusRejectsTerminalTransition(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("pi_failed", "card").
		WillReturnRows(sqlmock.NewRows([]string{"id", "invoice_id", "tenant_id", "amount", "currency", "status"}).
			AddRow("payment-1", "invoice-1", "tenant-1", "12.50", "EUR", "failed"))
	mock.ExpectExec(`pg_advisory_xact_lock`).WithArgs("invoice-1").WillReturnResult(sqlmock.NewResult(0, 0))
	expectLockedPaymentStatus(mock, "payment-1", "tenant-1", "failed")
	mock.ExpectRollback()

	updated, err := s.updateInvoicePaymentStatus("stripe", "pi_failed", "invoice-1", "confirmed", nil, providerSettlementEvidence{
		TenantID: "tenant-1", AmountCents: 1250, Currency: "EUR",
	})
	if err == nil || updated {
		t.Fatalf("terminal transition = updated %v, error %v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectLockedPaymentStatus(mock sqlmock.Sqlmock, paymentID, tenantID, status string) {
	mock.ExpectQuery(`SELECT payment\.status`).WithArgs(paymentID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
}

func TestUpdateInvoicePaymentStatusDoesNotFallbackForUnknownConfirmedTransaction(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	s := &Service{db: mockDB, logger: logrus.New()}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT payment\.id::text AS payment_id, payment\.invoice_id::text AS invoice_id`).
		WithArgs("pi_unknown", "card").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	updated, err := s.updateInvoicePaymentStatus("stripe", "pi_unknown", "invoice-1", "confirmed", nil, providerSettlementEvidence{
		TenantID: "tenant-1", AmountCents: 1250, Currency: "EUR",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated {
		t.Fatal("unknown provider transaction must not settle a pending invoice payment")
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
	appconfigtest.Set(t, "STRIPE_WEBHOOK_SECRET", "")

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
	appconfigtest.Set(t, "STRIPE_WEBHOOK_SECRET", "unit-test-secret")

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
	appconfigtest.Set(t, "STRIPE_WEBHOOK_SECRET", "unit-test-secret")

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
