package handlers

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestHandlePrepaidCheckoutCompletedRejectsTenantMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() {
		db = nil
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id FROM purser.pending_topups WHERE id = \\$1 FOR UPDATE").
		WithArgs("topup-123").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id"}).AddRow("pending", "tenant-a"))
	mock.ExpectRollback()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-1", "pi-1", "tenant-b", "topup-123", 1500, "EUR", ProviderStripe); err == nil {
		t.Fatal("expected error")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedSkipsAlreadyProcessed(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() {
		db = nil
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id FROM purser.pending_topups WHERE id = \\$1 FOR UPDATE").
		WithArgs("topup-456").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id"}).AddRow("completed", "tenant-a"))
	mock.ExpectRollback()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-2", "pi-2", "tenant-a", "topup-456", 1500, "USD", ProviderStripe); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedCreditsBalanceWithIdempotencyKey(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() {
		db = nil
	})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT status, tenant_id FROM purser.pending_topups WHERE id = \\$1 FOR UPDATE").
		WithArgs("topup-789").
		WillReturnRows(sqlmock.NewRows([]string{"status", "tenant_id"}).AddRow("pending", "tenant-a"))
	mock.ExpectExec("UPDATE purser.pending_topups").
		WithArgs("pay-3", "sess-3", "topup-789").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id, balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-a", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents"}).AddRow("balance-1", int64(500)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(2000), "balance-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO purser.balance_transactions`).
		WithArgs("tenant-a", int64(1500), int64(2000), "Card top-up via mollie", "topup-789", "mollie checkout completed", "sess-3").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-1"))
	mock.ExpectExec("UPDATE purser.pending_topups").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.payment_provider_intents").
		WithArgs("pay-3", "sess-3", sqlmock.AnyArg(), "topup-789").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.tenant_subscriptions").
		WithArgs("tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-3", "pay-3", "tenant-a", "topup-789", 1500, "EUR", ProviderMollie); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedRequiresTenantAndTopup(t *testing.T) {
	logger = logrus.New()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-3", "", "", "", 1500, "USD", ProviderStripe); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestMakeMollieAPICall_InlineDecodeErrors(t *testing.T) {
	withDefaultTransport(t, testRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, `{"id":`), nil
	}))

	_, err := makeMollieAPICall(context.Background(), http.MethodPost, "https://mollie.test/v2/payments", []byte(`{}`), "test-key", "")
	if err == nil || !strings.Contains(err.Error(), "failed to parse Mollie response") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestMakeMollieAPICall_MapsInlineResponse(t *testing.T) {
	expiresAt := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	withDefaultTransport(t, testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Idempotency-Key"); got != "idem-123" {
			t.Fatalf("expected idempotency key header, got %q", got)
		}
		return newJSONResponse(http.StatusOK, `{
			"id":"tr_123",
			"status":"open",
			"expiresAt":"`+expiresAt.Format(time.RFC3339)+`",
			"_links":{"checkout":{"href":"https://checkout.mollie.test/pay/tr_123"}}
		}`), nil
	}))

	resp, err := makeMollieAPICall(context.Background(), http.MethodPost, "https://mollie.test/v2/payments", []byte(`{}`), "test-key", "idem-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "tr_123" {
		t.Fatalf("unexpected ID: %q", resp.ID)
	}
	if resp.Status != "open" {
		t.Fatalf("unexpected status: %q", resp.Status)
	}
	if resp.CheckoutURL != "https://checkout.mollie.test/pay/tr_123" {
		t.Fatalf("unexpected checkout URL: %q", resp.CheckoutURL)
	}
	if !resp.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("unexpected expires_at: got %v, want %v", resp.ExpiresAt, expiresAt)
	}
}

func TestDispatchStripeCheckoutCompleted_MalformedJSON(t *testing.T) {
	err := DispatchStripeCheckoutCompleted(context.Background(), []byte(`{"id":`))
	if err == nil || !strings.Contains(err.Error(), "failed to parse checkout session") {
		t.Fatalf("expected checkout session parse error, got %v", err)
	}
}

func TestHandleSubscriptionCheckoutCompletedPersistsTierAndPaymentMethod(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() { db = nil })

	mock.ExpectExec(subscriptionCheckoutUpdatePattern()).
		WithArgs("cus_123", "sub_456", "tier-pro", "tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := handleSubscriptionCheckoutCompleted(
		context.Background(),
		"cs_test_session",
		"tenant-a",
		"tier-pro",
		"cus_123",
		"sub_456",
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandleSubscriptionCheckoutCompletedErrorsOnMissingRow(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer mockDB.Close()

	db = mockDB
	logger = logrus.New()
	t.Cleanup(func() { db = nil })

	mock.ExpectExec(subscriptionCheckoutUpdatePattern()).
		WithArgs("cus_123", "sub_456", "tier-pro", "tenant-missing").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = handleSubscriptionCheckoutCompleted(
		context.Background(),
		"cs_test_session",
		"tenant-missing",
		"tier-pro",
		"cus_123",
		"sub_456",
	)
	if err == nil || !strings.Contains(err.Error(), "no tenant_subscriptions row") {
		t.Fatalf("expected missing-row error, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandleSubscriptionCheckoutCompletedSkipsWhenTenantMissing(t *testing.T) {
	logger = logrus.New()

	if err := handleSubscriptionCheckoutCompleted(
		context.Background(),
		"cs_test_session",
		"",
		"tier-pro",
		"cus_123",
		"sub_456",
	); err != nil {
		t.Fatalf("expected nil error when tenant_id empty, got %v", err)
	}
}

func subscriptionCheckoutUpdatePattern() string {
	return `(?s)UPDATE purser\.tenant_subscriptions.*` +
		`tier_id = COALESCE\(\s*NULLIF\(\$3, ''\)::uuid,\s*CASE WHEN pending_reason = 'stripe_checkout' THEN pending_tier_id END,\s*tier_id\s*\).*` +
		`pending_tier_id = CASE.*pending_reason = 'stripe_checkout'.*pending_tier_id = NULLIF\(\$3, ''\)::uuid.*` +
		`pending_effective_at = CASE.*pending_reason = 'stripe_checkout'.*pending_tier_id = NULLIF\(\$3, ''\)::uuid.*` +
		`pending_reason = CASE.*pending_reason = 'stripe_checkout'.*pending_tier_id = NULLIF\(\$3, ''\)::uuid.*` +
		`WHERE tenant_id = \$4`
}

func withDefaultTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
}
