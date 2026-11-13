//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
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
	assertCheckoutWakePreservesActiveClaim(t, db)
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
	if err := store.RecordFailureToken(ctx, claims[0].ID, 0, nil, errors.New("credential=must-not-persist"), 0, claims[0].LeaseToken); err != nil {
		t.Fatalf("record failure: %v", err)
	}

	var failureCode, failureMessage string
	if err := db.QueryRowContext(ctx, `SELECT last_error_code,last_error FROM purser.provider_webhook_inbox WHERE id=$1`, claims[0].ID).Scan(&failureCode, &failureMessage); err != nil {
		t.Fatal(err)
	}
	if failureCode != "reconciliation_failed" || failureMessage != "provider webhook reconciliation failed" {
		t.Fatalf("raw provider error persisted: %q %q", failureCode, failureMessage)
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
	var recoveryTenant string
	if err := db.QueryRowContext(ctx, `SELECT headers->>'tenant_id' FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+intentID).Scan(&recoveryTenant); err != nil || recoveryTenant != tenantID {
		t.Fatalf("checkout expiry tenant header = %q, want %q: %v", recoveryTenant, tenantID, err)
	}
	// Native callbacks are opaque bytes, including Mollie form bodies. Inventory
	// must not decode them as internal checkout JSON before filtering by tenant.
	for i, raw := range [][]byte{[]byte("id=tr_opaque"), {0xfc, 0xff}} {
		if ok, _, _ := service.enqueueProviderWebhook(ctx, "mollie", "opaque-"+string(rune('a'+i)), nil, raw); !ok {
			t.Fatal("enqueue opaque provider callback")
		}
	}
	items, listErr := purserdb.New(db).ListCheckoutRecovery(ctx, purserdb.ListCheckoutRecoveryParams{TenantID: tenantID, RowLimit: 10})
	if listErr != nil || len(items) != 1 || items[0].Provider != "stripe" {
		t.Fatalf("operator inventory must name the payment provider, not the internal work kind: %+v %v", items, listErr)
	}
	all, listErr := purserdb.New(db).ListCheckoutRecovery(ctx, purserdb.ListCheckoutRecoveryParams{RowLimit: 10})
	if listErr != nil || len(all) != 4 {
		t.Fatalf("opaque callbacks broke global recovery inventory: rows=%d err=%v", len(all), listErr)
	}
	unresolvedTenantID, unresolvedIntentID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key) VALUES($1::text::uuid,$2,'stripe','tenant_subscription_checkout','pending','EUR',$1::text)`, unresolvedIntentID, unresolvedTenantID); err != nil {
		t.Fatal(err)
	}
	if err := purserdb.New(db).EnqueueUnresolvedCheckoutRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT headers->>'tenant_id' FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+unresolvedIntentID).Scan(&recoveryTenant); err != nil || recoveryTenant != unresolvedTenantID {
		t.Fatalf("unresolved checkout tenant header = %q, want %q: %v", recoveryTenant, unresolvedTenantID, err)
	}
	items, listErr = purserdb.New(db).ListCheckoutRecovery(ctx, purserdb.ListCheckoutRecoveryParams{TenantID: unresolvedTenantID, RowLimit: 10})
	if listErr != nil || len(items) != 1 || items[0].ReservationID != unresolvedIntentID {
		t.Fatalf("unresolved checkout absent from tenant inventory: %+v %v", items, listErr)
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

func assertCheckoutWakePreservesActiveClaim(t *testing.T, db *sql.DB) {
	t.Helper()
	tenant, id := uuid.NewString(), uuid.NewString()
	tier := uuid.NewString()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.billing_tiers(id,tier_name,display_name) VALUES($1::uuid,$1::text,'Bind fixture')`, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model) VALUES($1,$2,'active','postpaid')`, tenant, tier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,recovery_started_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','cancelled','EUR',$1::text,NOW())`, id, tenant); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	if err := q.RequestCheckoutRecovery(t.Context(), purserdb.RequestCheckoutRecoveryParams{TenantID: tenant, IntentID: id, Reason: "operator recovery"}); err != nil {
		t.Fatal(err)
	}
	store := &providerWebhookInboxStore{db: db}
	claims, err := store.ClaimBatch(t.Context(), 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("initial claim: %+v %v", claims, err)
	}
	if err = q.SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant, IntentID: id, SessionID: sql.NullString{String: "late-session", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	var state, token string
	if err = db.QueryRowContext(t.Context(), `SELECT status,COALESCE(lease_token::text,'') FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1 AND headers->>'tenant_id'=$2`, "checkout-expiry:"+id, tenant).Scan(&state, &token); err != nil {
		t.Fatal(err)
	}
	if state != "processing" || token != claims[0].LeaseToken {
		t.Fatalf("late receipt stole active recovery claim: state=%s token=%s expected=%s", state, token, claims[0].LeaseToken)
	}
	if err = store.MarkCompletedToken(t.Context(), claims[0].ID, claims[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	followup, err := store.ClaimBatch(t.Context(), 1, time.Minute)
	if err != nil || len(followup) != 1 {
		t.Fatalf("old acknowledgement lost late receipt wake: %+v %v", followup, err)
	}
	if rows, err := q.FinishCheckoutCompensation(t.Context(), purserdb.FinishCheckoutCompensationParams{TenantID: tenant, IntentID: id, Outcome: "cancelled", ExpectedReferences: []byte(`{"session_id":"late-session","payment_id":null,"subscription_id":null}`)}); err != nil || rows != 1 {
		t.Fatalf("finish first reference snapshot: rows=%d err=%v", rows, err)
	}
	svc := &Service{db: db}
	if expired, err := svc.bindCheckoutReceipt(t.Context(), tenant, id, "stripe", "late-session", "", "late-subscription"); err != nil || !expired {
		t.Fatalf("bind late webhook: expired=%v err=%v", expired, err)
	}
	if err = store.MarkCompletedToken(t.Context(), followup[0].ID, followup[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	next, err := store.ClaimBatch(t.Context(), 1, time.Minute)
	if err != nil || len(next) != 1 {
		t.Fatalf("old acknowledgement lost bound webhook references: %+v %v", next, err)
	}
	if err = store.MarkCompletedToken(t.Context(), next[0].ID, next[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
}
