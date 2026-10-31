//go:build schema_verify

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/subscriptions"

	"github.com/google/uuid"
)

// TestIneligibleCheckoutCompensates exercises production recovery and its provider lock boundary.
func TestIneligibleCheckoutCompensates_RealPG(t *testing.T) {
	for _, mode := range []string{"stripe", "mollie", "native duplicate", "async Stripe refusal", "advance refusal", "concurrent subscription change"} {
		t.Run("ineligible recovery/"+mode, func(t *testing.T) {
			provider := mode
			if mode == "native duplicate" || mode == "async Stripe refusal" || mode == "advance refusal" || mode == "concurrent subscription change" {
				provider = "stripe"
			}
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now, now.Add(30*24*time.Hour), 0)
			target := tenant.supporterID
			wantModel := "prepaid"
			if mode == "native duplicate" {
				target = insertTierChangeTier(t, db, "50")
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='postpaid',payment_method='stripe',stripe_customer_id='customer',stripe_subscription_id='established',stripe_subscription_status='active' WHERE tenant_id=$1`, tenant.id, tenant.supporterID); err != nil {
					t.Fatal(err)
				}
				wantModel = "postpaid"
			} else if mode == "advance refusal" {
				target = insertTierChangeTier(t, db, "50")
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='postpaid',payment_method='stripe',stripe_customer_id='customer' WHERE tenant_id=$1`, tenant.id, tenant.supporterID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET base_price=20 WHERE id=$1`, tenant.supporterID); err != nil {
					t.Fatal(err)
				}
				wantModel = "postpaid"
			} else if mode != "concurrent subscription change" {
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET is_enterprise=true WHERE id=$1`, target); err != nil {
					t.Fatal(err)
				}
			}

			id := uuid.NewString()
			fixture, client := installStripeFixture(t)
			purpose, session, payment, subscription := "tenant_subscription_checkout", "ineligible", "", "unactivated"
			if provider == "mollie" {
				purpose, session, payment, subscription = "mollie_first_payment", "", "initial", ""
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_session_id,provider_payment_id,provider_subscription_id,expires_at)
VALUES($1::uuid,$2,$3,$4,'billing_tiers',$5,'provider_open','EUR',2000,$1::text,'customer',NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NOW()+INTERVAL '1 hour')`, id, tenant.id, provider, purpose, target, session, payment, subscription); err != nil {
				t.Fatal(err)
			}
			if provider == "stripe" {
				tenant.svc.stripeClient = client
				fixture.route("GET", "/v1/checkout/sessions/ineligible", func(recordedProviderRequest) (int, any) {
					assertSubscriptionUnlockedDuringProviderCall(t, db, tenant.id)
					if mode == "concurrent subscription change" {
						if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET status='past_due' WHERE tenant_id=$1`, tenant.id); err != nil {
							t.Fatal(err)
						}
					}
					return 200, map[string]any{"id": "ineligible", "status": "complete", "expires_at": now.Add(time.Hour).Unix(), "customer": "customer", "subscription": "unactivated", "payment_status": "paid", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": target, "checkout_reservation_id": id}}
				})
				fixture.route("GET", "/v1/subscriptions/unactivated", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "unactivated", "status": "active"}
				})
				fixture.route("DELETE", "/v1/subscriptions/unactivated", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "unactivated", "status": "canceled"}
				})
				fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "initial", "billing_reason": "subscription_create", "status": "paid", "amount_paid": 2000}}, "has_more": false}
				})
				fixture.route("GET", "/v1/invoice_payments", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "initial-paid", "amount_paid": 2000, "payment": map[string]any{"type": "charge", "charge": "initial-charge"}}}, "has_more": false}
				})
				fixture.route("GET", "/v1/charges/initial-charge", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "initial-charge", "amount": 2000, "currency": "eur", "amount_refunded": 0}
				})
				fixture.route("GET", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
					if r.query.Get("charge") == "" {
						t.Error("refund history was not scoped to the purchase charge")
					}
					return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
				})
				fixture.route("POST", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
					if r.form.Get("amount") != "2000" {
						t.Errorf("wrong initial refund: %v", r.form)
					}
					return 200, map[string]any{"id": "initial-refund", "status": "pending"}
				})
			} else {
				fixture, tenant.svc.mollieClient = installMollieFixture(t)
				fixture.route("GET", "/v2/payments/initial", func(recordedProviderRequest) (int, any) {
					assertSubscriptionUnlockedDuringProviderCall(t, db, tenant.id)
					return 200, map[string]any{"id": "initial", "status": "paid", "sequenceType": "first", "customerId": "customer", "mandateId": "mandate", "paidAt": now.Format(time.RFC3339), "createdAt": now.Add(-time.Minute).Format(time.RFC3339), "amount": map[string]string{"value": "20.00", "currency": "EUR"}, "metadata": map[string]string{"tenant_id": tenant.id, "tier_id": target, "payment_type": "first_payment", "checkout_reservation_id": id}}
				})
				fixture.route("GET", "/v2/customers/customer/mandates/mandate", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "mandate", "status": "valid", "method": "creditcard"}
				})
				fixture.route("GET", "/v2/customers/customer/subscriptions", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"_embedded": map[string]any{"subscriptions": []any{}}, "_links": map[string]any{}}
				})
				fixture.route("POST", "/v2/payments/initial/refunds", func(recordedProviderRequest) (int, any) {
					return 201, map[string]any{"id": "initial-refund", "status": "pending"}
				})
			}

			fixture.beforeRequest = func() { assertSubscriptionUnlockedDuringProviderCall(t, db, tenant.id) }
			expiry := checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}
			if mode == "async Stripe refusal" {
				var event StripeWebhookPayload
				raw, _ := json.Marshal(map[string]any{"id": "event", "created": now.Unix(), "type": "customer.subscription.updated", "data": map[string]any{"object": map[string]any{"id": "unactivated", "customer": "customer", "status": "active", "metadata": map[string]string{"tenant_id": tenant.id, "tier_id": target, "checkout_reservation_id": id}}}})
				if err := json.Unmarshal(raw, &event); err != nil {
					t.Fatal(err)
				}
				if err := tenant.svc.handleStripeSubscriptionEvent(event); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "advance refusal" {
				if err := tenant.svc.activateAdvanceBilledTenant(t.Context(), tenant.id, target, "stripe", "customer", now.Add(time.Second), id); !errors.Is(err, subscriptions.ErrCheckoutExpired) {
					t.Fatalf("advance refusal did not classify compensation: %v", err)
				}
			}
			if mode == "native duplicate" {
				if err := tenant.svc.handleSubscriptionCheckoutCompleted(t.Context(), session, tenant.id, target, "customer", "unactivated", true, id); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "native duplicate" || mode == "async Stripe refusal" || mode == "advance refusal" {
				var payload []byte
				var latched bool
				if err := db.QueryRowContext(t.Context(), `SELECT i.raw_payload,p.recovery_started_at IS NOT NULL FROM purser.provider_webhook_inbox i JOIN purser.payment_provider_intents p ON i.event_key='checkout-expiry:'||p.id::text WHERE p.tenant_id=$1 AND p.id=$2 AND i.provider='checkout_recovery'`, tenant.id, id).Scan(&payload, &latched); err != nil || !latched {
					t.Fatalf("refused native completion did not durably queue compensation: latched=%t err=%v", latched, err)
				}
				if err := json.Unmarshal(payload, &expiry); err != nil || !expiry.Force {
					t.Fatalf("compensation retained original 23h deadline: expiry=%+v err=%v", expiry, err)
				}
			}
			if mode == "concurrent subscription change" {
				err := tenant.svc.expireCheckoutReservation(t.Context(), expiry)
				if !errors.Is(err, ErrAwaitingProviderSettlement) || len(fixture.matching("POST", "/v1/refunds")) != 0 || len(fixture.matching("DELETE", "/v1/subscriptions/unactivated")) != 0 {
					t.Fatalf("recovery acted on stale subscription snapshot: err=%v", err)
				}
				return
			}
			if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); err != nil {
				t.Fatal(err)
			}
			var actual, outcome string
			if err := db.QueryRowContext(t.Context(), `SELECT s.billing_model,COALESCE(p.recovery_outcome,'') FROM purser.tenant_subscriptions s JOIN purser.payment_provider_intents p ON p.tenant_id=s.tenant_id WHERE s.tenant_id=$1 AND p.id=$2`, tenant.id, id).Scan(&actual, &outcome); err != nil {
				t.Fatal(err)
			}
			wantOutcome := "cancelled_refund_requested"
			if provider == "mollie" {
				wantOutcome = "refund_requested"
			}
			if actual != wantModel || outcome != wantOutcome {
				t.Fatalf("ineligible purchase was reactivated or left pending: model=%s outcome=%s", actual, outcome)
			}
			path := "/v1/refunds"
			if provider == "mollie" {
				path = "/v2/payments/initial/refunds"
			}
			if count := len(fixture.matching("POST", path)); count != 1 {
				t.Fatalf("initial purchase compensation count=%d", count)
			}
			if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); err != nil {
				t.Fatal(err)
			}
			if count := len(fixture.matching("POST", path)); count != 1 {
				t.Fatalf("compensation repeated: %d", count)
			}
		})
	}
}

func assertSubscriptionUnlockedDuringProviderCall(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Error(err)
		return
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRowContext(t.Context(), `SELECT tenant_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1 FOR UPDATE NOWAIT`, tenantID).Scan(&id); err != nil {
		t.Errorf("provider call held the subscription row lock: %v", err)
	}
}
