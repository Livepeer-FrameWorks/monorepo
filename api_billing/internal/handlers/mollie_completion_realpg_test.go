//go:build schema_verify

package handlers

import (
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestMollieCompletionSurvivesLateSettlement_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installMollieFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now.Add(-48*time.Hour), now.AddDate(0, 1, 0), 0)
	tenant.svc.mollieClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,provider_customer_id,provider_payment_id,expires_at,amount_cents) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'provider_open','EUR',$1::text,'customer','payment',NOW()-INTERVAL '1 hour',2000)`, id, tenant.id, tenant.supporterID); err != nil {
		t.Fatal(err)
	}
	state := "pending"
	fixture.route("GET", "/v2/payments/payment", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "payment", "status": state, "paidAt": now.Format(time.RFC3339), "sequenceType": "first", "customerId": "customer", "mandateId": "mandate", "amount": map[string]string{"currency": "EUR", "value": "20.00"}, "metadata": map[string]string{"tenant_id": tenant.id, "tier_id": tenant.supporterID, "payment_type": "first_payment", "checkout_reservation_id": id}}
	})
	fixture.route("GET", "/v2/customers/customer/mandates/mandate", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "mandate", "status": "valid", "method": "directdebit", "createdAt": now.Format(time.RFC3339)}
	})
	expiry := checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}
	if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); !errors.Is(err, ErrAwaitingProviderSettlement) && !errors.Is(err, billingmollie.ErrPaymentSettling) {
		t.Fatalf("accepted payment was not retained for settlement: %v", err)
	}
	q := purserdb.New(db)
	r, err := q.GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
	if err != nil || !r.CheckoutCompletedAt.Valid || r.RecoveryStartedAt.Valid {
		t.Fatalf("completion rolled back while waiting: %+v %v", r, err)
	}
	if pending, err := q.GetPendingProviderCheckout(t.Context(), purserdb.GetPendingProviderCheckoutParams{TenantID: tenant.id}); err != nil || !pending.Pending {
		t.Fatalf("settling purchase lost fence: %v %v", pending, err)
	}
	state = "paid"
	// Another worker has already claimed this provider status, but has not yet
	// installed its receipt. A nil callback result must keep recovery pending.
	payment, err := client.GetPayment(t.Context(), "payment")
	if err != nil {
		t.Fatal(err)
	}
	eventID := mollieEventIDForPayment(payment, state)
	claim, err := tenant.svc.claimWebhookEvent(t.Context(), "mollie", eventID, "payment", "", []byte("id=payment"))
	if err != nil || !claim.claimed {
		t.Fatalf("claim: %+v %v", claim, err)
	}
	if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); !errors.Is(err, ErrAwaitingProviderSettlement) {
		t.Fatalf("uninstalled concurrent receipt was acknowledged: %v", err)
	}
	if _, err := db.Exec(`UPDATE purser.webhook_events SET status='failed_retryable' WHERE provider='mollie' AND event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); err != nil {
		t.Fatal(err)
	}
	r, err = q.GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("late settlement did not install receipt: %s %v", r.Status, err)
	}
	if len(fixture.matching("POST", "/v2/payments/payment/refunds")) != 0 {
		t.Fatal("on-time completed payment was refunded")
	}
}
