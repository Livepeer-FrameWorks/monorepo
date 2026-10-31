package handlers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestProviderWebhookInboxClaimUsesTokenFence(t *testing.T) {
	const inboxID = "00000000-0000-0000-0000-000000000001"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.provider_webhook_inbox[\s\S]+FOR UPDATE SKIP LOCKED`).
		WithArgs(int64(120000), 10).
		WillReturnRows(sqlmock.NewRows([]string{"id", "provider", "headers", "raw_payload", "attempts", "created_at", "event_key"}).
			AddRow(inboxID, "stripe", []byte(`{"Stripe-Signature":"sig"}`), []byte(`{"id":"evt_1"}`), 2, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC), "evt_1"))
	mock.ExpectExec(`UPDATE purser\.provider_webhook_inbox[\s\S]+status = 'processing'`).
		WithArgs(sqlmock.AnyArg(), inboxID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	claims, err := (&providerWebhookInboxStore{db: db}).ClaimBatch(context.Background(), 10, 2*time.Minute)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(claims) != 1 || claims[0].ID != inboxID || claims[0].LeaseToken == "" || claims[0].Attempts != 2 {
		t.Fatalf("claims = %+v", claims)
	}
	if claims[0].Payload.Provider != "stripe" || claims[0].Payload.Headers["Stripe-Signature"] != "sig" {
		t.Fatalf("payload = %+v", claims[0].Payload)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestProviderWebhookInboxCompletionRequiresLease(t *testing.T) {
	const inboxID = "00000000-0000-0000-0000-000000000001"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectExec(`UPDATE purser\.provider_webhook_inbox[\s\S]+lease_token::text = \$2`).
		WithArgs(inboxID, "lease-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := (&providerWebhookInboxStore{db: db}).MarkCompletedToken(context.Background(), inboxID, "lease-1"); err == nil {
		t.Fatal("expected stale lease error")
	}
}

func TestProviderWebhookHeadersRetainsOnlyReconciliationHeaders(t *testing.T) {
	got := providerWebhookHeaders(map[string]string{
		"stripe-signature": "sig",
		"Content-Type":     "application/json",
		"Authorization":    "must-not-persist",
		"Cookie":           "must-not-persist",
	})
	if len(got) != 2 || got["Stripe-Signature"] != "sig" || got["Content-Type"] != "application/json" {
		t.Fatalf("providerWebhookHeaders = %#v", got)
	}
}

func TestProviderInboxMetricsRetainLastGoodCountOnReadFailure(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	metrics := &PurserMetrics{ProviderWebhookInboxFailures: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_provider_inbox_items"}, nil), ProviderWebhookInboxReadFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_provider_inbox_read_failures_total"}, nil)}
	store := &providerWebhookInboxStore{db: db, metrics: metrics}
	m.ExpectQuery("ProviderWebhookInboxRepeatedFailures").WillReturnRows(sqlmock.NewRows([]string{"items"}).AddRow(2))
	store.observeFailures(context.Background())
	m.ExpectQuery("ProviderWebhookInboxRepeatedFailures").WillReturnError(errors.New("database unavailable"))
	store.observeFailures(context.Background())
	if got := testutil.ToFloat64(metrics.ProviderWebhookInboxFailures.WithLabelValues()); got != 2 {
		t.Fatalf("failed read erased known stuck work: %v", got)
	}
	if got := testutil.ToFloat64(metrics.ProviderWebhookInboxReadFailures.WithLabelValues()); got != 1 {
		t.Fatalf("read failure count=%v", got)
	}
	m.ExpectQuery("ProviderWebhookInboxRepeatedFailures").WillReturnRows(sqlmock.NewRows([]string{"items"}).AddRow(0))
	store.observeFailures(context.Background())
	if got := testutil.ToFloat64(metrics.ProviderWebhookInboxFailures.WithLabelValues()); got != 0 {
		t.Fatalf("resolved recovery still counted: %v", got)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Dispatch is sequential, so a claim must not reserve work beyond its lease budget.
func TestProviderInboxClaimsOneBoundedDispatchPerLease(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m.MatchExpectationsInOrder(false)
	expectCheckoutRecoverySessionLock(m)
	expectCheckoutRecoverySessionUnlock(m)
	metrics := &PurserMetrics{ProviderWebhookInboxFailures: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_worker_inbox_items"}, nil), ProviderWebhookInboxReadFailures: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_worker_inbox_read_failures_total"}, nil)}
	m.ExpectQuery("ProviderWebhookInboxRepeatedFailures").WillReturnRows(sqlmock.NewRows([]string{"items"}).AddRow(2))
	m.ExpectExec("EnqueueUnresolvedCheckoutRecovery").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectBegin()

	const inboxID = "00000000-0000-0000-0000-000000000001"
	m.ExpectQuery("ClaimProviderWebhooks").WithArgs(int64(120000), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "provider", "headers", "raw_payload", "attempts", "created_at", "event_key"}).AddRow(inboxID, "checkout_recovery", []byte(`{}`), []byte(`{"checkout_reservation_id":"intent","tenant_id":"tenant"}`), 0, time.Now(), "checkout-expiry:intent"))
	m.ExpectExec("MarkProviderWebhookProcessing").WithArgs(sqlmock.AnyArg(), inboxID).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "succeeded", time.Now().Add(-time.Minute), 0))
	m.ExpectCommit()
	m.ExpectBegin()
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "succeeded", time.Now().Add(-time.Minute), 0))
	m.ExpectQuery("PendingCheckoutRecoveryAudits").WillReturnRows(sqlmock.NewRows([]string{"id", "actor_id", "reason"}))
	m.ExpectCommit()
	m.ExpectExec("CompleteProviderWebhook").WithArgs(inboxID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectBegin()
	m.ExpectQuery("ClaimProviderWebhooks").WithArgs(int64(120000), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "provider", "headers", "raw_payload", "attempts", "created_at", "event_key"}))
	m.ExpectCommit()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{db: db, metrics: metrics}}
	jm.runProviderWebhookInbox(ctx)
	if got := testutil.ToFloat64(metrics.ProviderWebhookInboxFailures.WithLabelValues()); got != 2 {
		t.Fatalf("actual worker did not sample its recovery inventory: %v", got)
	}
	if got := testutil.ToFloat64(metrics.ProviderWebhookInboxReadFailures.WithLabelValues()); got != 0 {
		t.Fatalf("inventory was read again on the claim path: %v", got)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatalf("provider inbox claimed more I/O than its lease can cover: %v", err)
	}
}

func TestMollieBodyCannotSelectCheckoutRecovery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := &providerWebhookDispatcher{service: &Service{db: db}}
	_, err = d.Dispatch(context.Background(), providerWebhookPayload{Provider: "mollie", EventKey: "checkout-expiry:invented", Headers: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"checkout_reservation_id":"invented","tenant_id":"invented"}`)})
	if err == nil || !strings.Contains(err.Error(), "has no payment id") {
		t.Fatalf("outside input reached internal recovery: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutRecoveryRetryDiscardsRolledBackDecision(t *testing.T) {
	db, m, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deadline := time.Now().Add(-time.Hour)
	expectCheckoutRecoverySessionLock(m)
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "succeeded", deadline, 0))
	m.ExpectCommit().WillReturnError(&pq.Error{Code: "40001", Message: "retry transaction"})
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "pending", deadline, 0))
	m.ExpectCommit()
	m.ExpectBegin()
	expectProviderSubscriptionLock(m, "tenant")
	m.ExpectQuery("GetProviderCheckoutReservation").WillReturnRows(checkoutReservationTestRows("intent", "tenant", "stripe", "tenant_subscription_checkout", nil, nil, nil, nil, "pending", deadline, 0))
	m.ExpectExec("StartCheckoutCompensation").WillReturnResult(sqlmock.NewResult(0, 1))
	m.ExpectCommit()
	m.ExpectQuery("FinishCheckoutCompensation").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))
	expectCheckoutRecoverySessionUnlock(m)
	if err = (&Service{db: db}).expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: "tenant", IntentID: "intent"}); err != nil {
		t.Fatal(err)
	}
	if err = m.ExpectationsWereMet(); err != nil {
		t.Fatalf("rolled-back completion skipped recovery: %v", err)
	}
}
