//go:build schema_verify

package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/google/uuid"
)

func TestProviderWebhookInboxRepository_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	store := &providerWebhookInboxStore{db: db}
	service := &Service{db: db}

	ok, message, status := service.enqueueProviderWebhook(ctx, "stripe", "evt-contract", map[string]string{
		"Stripe-Signature": "sig", "Authorization": "must-not-persist",
	}, []byte(`{"id":"evt-contract"}`))
	if !ok || message != "" || status != 200 {
		t.Fatalf("enqueue = (%v, %q, %d)", ok, message, status)
	}
	if ok, _, status := service.enqueueProviderWebhook(ctx, "stripe", "evt-contract", nil, []byte(`{}`)); !ok || status != 200 {
		t.Fatalf("duplicate enqueue = (%v, %d)", ok, status)
	}

	claims, err := store.ClaimBatch(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 1 || claims[0].Payload.Provider != "stripe" || claims[0].Payload.Headers["Authorization"] != "" {
		t.Fatalf("claims = %#v", claims)
	}
	if _, err := uuid.Parse(claims[0].ID); err != nil {
		t.Fatalf("claim id is not UUID: %v", err)
	}
	if _, err := uuid.Parse(claims[0].LeaseToken); err != nil {
		t.Fatalf("lease token is not UUID: %v", err)
	}

	if err := store.MarkCompletedToken(ctx, claims[0].ID, uuid.NewString()); err == nil {
		t.Fatal("stale completion lease unexpectedly succeeded")
	}
	if err := store.RecordFailureToken(ctx, claims[0].ID, 0, nil, errors.New("retry me"), 0, claims[0].LeaseToken); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	retry, err := store.ClaimBatch(ctx, 10, 0)
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}

	if len(retry) == 1 && !retry[0].Payload.ReceivedAt.Equal(claims[0].Payload.ReceivedAt) {
		t.Fatal("webhook retry moved the metering boundary")
	}
	if len(retry) != 1 || retry[0].Attempts != 1 {
		t.Fatalf("retry claims = %#v", retry)
	}
	if err := store.MarkCompletedToken(ctx, retry[0].ID, retry[0].LeaseToken); err != nil {
		t.Fatalf("complete: %v", err)
	}

	var statusValue string
	var attempts int
	if err := db.QueryRowContext(ctx, `SELECT status, attempts FROM purser.provider_webhook_inbox WHERE id = $1`, retry[0].ID).Scan(&statusValue, &attempts); err != nil {
		t.Fatal(err)
	}
	if statusValue != "processed" || attempts != 1 {
		t.Fatalf("final row = status %q attempts %d", statusValue, attempts)
	}
	if ok, _, _ := service.enqueueProviderWebhook(ctx, "mollie", "settling-contract", nil, []byte(`{"id":"payment"}`)); !ok {
		t.Fatal("enqueue settling work")
	}
	for n := 0; n < 3; n++ {
		claims, err := store.ClaimBatch(ctx, 1, time.Minute)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim settlement wait: %v %v", claims, err)
		}
		if err := store.RecordFailureToken(ctx, claims[0].ID, claims[0].Attempts, nil, billingmollie.ErrPaymentSettling, 0, claims[0].LeaseToken); err != nil {
			t.Fatal(err)
		}
	}
	failures, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(ctx)
	if err != nil || failures != 0 {
		t.Fatalf("normal settlement raised a failure alert: %d %v", failures, err)
	}

	tenantID, intentID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,expires_at) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout','pending','EUR',$1::text,NOW()+INTERVAL '1 hour')`, intentID, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).EnqueueCheckoutReservationExpiry(ctx, purserdb.EnqueueCheckoutReservationExpiryParams{TenantID: tenantID, IntentID: intentID}); err != nil {
		t.Fatal(err)
	}
	items, listErr := purserdb.New(db).ListCheckoutRecovery(ctx, purserdb.ListCheckoutRecoveryParams{TenantID: tenantID, RowLimit: 10})
	if listErr != nil || len(items) != 1 || items[0].Provider != "stripe" {
		t.Fatalf("operator inventory must name the payment provider, not the internal work kind: %+v %v", items, listErr)
	}
}

func TestProviderSettlementWaitBackoffAndAgeAlert_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := t.Context()
	store := &providerWebhookInboxStore{db: db}
	svc := &Service{db: db}
	if ok, _, _ := svc.enqueueProviderWebhook(ctx, "mollie", "wait-age", nil, []byte(`{"id":"payment"}`)); !ok {
		t.Fatal("enqueue wait")
	}
	for n := 0; n < 3; n++ {
		claims, err := store.ClaimBatch(ctx, 1, time.Minute)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim=%v %v", claims, err)
		}
		backoff := outbox.ComputeBackoff(providerWebhookWorkerConfig(), claims[0].Attempts)
		if backoff != time.Minute*time.Duration(1<<n) {
			t.Fatalf("wait backoff did not grow: attempt %d delay %s", n, backoff)
		}
		if err := store.RecordFailureToken(ctx, claims[0].ID, claims[0].Attempts, nil, ErrAwaitingProviderSettlement, backoff, claims[0].LeaseToken); err != nil {
			t.Fatal(err)
		}
		var failures, waits int
		var message string
		var future bool
		if err := db.QueryRow(`SELECT attempts,wait_attempts,last_error,next_attempt_at>NOW() FROM purser.provider_webhook_inbox WHERE id=$1`, claims[0].ID).Scan(&failures, &waits, &message, &future); err != nil || failures != 0 || waits != n+1 || message == "" || !future {
			t.Fatalf("wait failures=%d waits=%d message=%q future=%v err=%v", failures, waits, message, future, err)
		}
		if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE id=$1`, claims[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	q := purserdb.New(db)
	if count, err := q.ProviderWebhookInboxRepeatedFailures(ctx); err != nil || count != 0 {
		t.Fatalf("legitimate settlement raised alert: %d %v", count, err)
	}
	if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET unresolved_since=NOW()-INTERVAL '15 days' WHERE provider='mollie' AND event_key='wait-age'`); err != nil {
		t.Fatal(err)
	}
	if count, err := q.ProviderWebhookInboxRepeatedFailures(ctx); err != nil || count != 1 {
		t.Fatalf("stale settlement failed to alert: %d %v", count, err)
	}
}
