//go:build schema_verify

package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"
)

func TestAsyncFailureKeepsFenceUntilCancellation_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	tenant.svc.stripeClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_session_id,provider_subscription_id,expires_at,checkout_completed_at)
VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,'session','subscription',NOW()-INTERVAL '1 hour',NOW()-INTERVAL '2 hours')`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.provider_webhook_inbox(provider,event_key,headers,raw_payload,next_attempt_at) VALUES('checkout_recovery',$1,'{}',$2,NOW()+INTERVAL '6 hours')`, "checkout-expiry:"+id, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	event := StripeWebhookPayload{}
	event.Data.Object, _ = json.Marshal(map[string]any{"id": "session", "subscription": "subscription", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription"}})
	if err := tenant.svc.handleStripeCheckoutAsyncPaymentFailed(event); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	pending, err := q.GetPendingProviderCheckout(t.Context(), purserdb.GetPendingProviderCheckoutParams{TenantID: tenant.id})
	if err != nil || !pending.Pending {
		t.Fatalf("failed payment released fence before subscription cancellation: pending=%v err=%v", pending.Pending, err)
	}
	row, err := q.GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
	if err != nil || !row.RecoveryStartedAt.Valid {
		t.Fatalf("failed attempt did not start compensation: %+v err=%v", row, err)
	}
	var due bool
	if err := db.QueryRow(`SELECT next_attempt_at<=NOW() FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+id).Scan(&due); err != nil || !due {
		t.Fatalf("failed attempt left cancellation behind old backoff: due=%v err=%v", due, err)
	}
	fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "session", "status": "complete", "subscription": "subscription", "payment_status": "paid"}
	})
	fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "incomplete"}
	})
	fixture.route("DELETE", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 503, map[string]any{"error": map[string]string{"type": "api_error", "message": "temporarily unavailable"}}
	})
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); err == nil {
		t.Fatal("provider cancellation failure was acknowledged")
	}
	pending, err = q.GetPendingProviderCheckout(t.Context(), purserdb.GetPendingProviderCheckoutParams{TenantID: tenant.id})
	if err != nil || !pending.Pending || len(fixture.matching("POST", "/v1/refunds")) != 0 {
		t.Fatalf("failed cancellation released fence or refunded before cancellation: %+v %v", pending, err)
	}
	fixture.route("DELETE", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "canceled"}
	})
	fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
	})
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); err != nil {
		t.Fatal(err)
	}
	pending, err = q.GetPendingProviderCheckout(t.Context(), purserdb.GetPendingProviderCheckoutParams{TenantID: tenant.id})
	if err != nil || pending.Pending {
		t.Fatalf("confirmed cancellation retained fence: %+v %v", pending, err)
	}
}
