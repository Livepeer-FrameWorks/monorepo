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

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-1", "tenant-b", "topup-123", 1500, "EUR", ProviderStripe); err == nil {
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

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-2", "tenant-a", "topup-456", 1500, "USD", ProviderStripe); err != nil {
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
	mock.ExpectQuery("SELECT id, balance_cents FROM purser.prepaid_balances").
		WithArgs("tenant-a", "EUR").
		WillReturnRows(sqlmock.NewRows([]string{"id", "balance_cents"}).AddRow("balance-1", int64(500)))
	mock.ExpectExec("UPDATE purser.prepaid_balances").
		WithArgs(int64(2000), "balance-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO purser.balance_transactions .*reference_type.*VALUES \(\$1, \$2, \$3, 'topup', \$4, \$5, 'topup'\)`).
		WithArgs("tenant-a", int64(1500), int64(2000), "Card top-up via mollie", "topup-789").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("tx-1"))
	mock.ExpectExec("UPDATE purser.pending_topups").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE purser.tenant_subscriptions").
		WithArgs("tenant-a").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-3", "tenant-a", "topup-789", 1500, "EUR", ProviderMollie); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestHandlePrepaidCheckoutCompletedRequiresTenantAndTopup(t *testing.T) {
	logger = logrus.New()

	if err := handlePrepaidCheckoutCompleted(context.Background(), "sess-3", "", "", 1500, "USD", ProviderStripe); err != nil {
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

func withDefaultTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	old := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old })
}
