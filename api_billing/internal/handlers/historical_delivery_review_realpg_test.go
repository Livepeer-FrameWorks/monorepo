//go:build schema_verify

package handlers

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/google/uuid"
)

func assertAutomaticHistoricalDeliveryReview(t *testing.T) {
	for _, kind := range []string{
		"delivered", "delivered with bookkeeping write", "delivered with concurrent subscription change",
		"unproven subscription", "no subscription",
	} {
		t.Run(kind, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			fixture, client := installMollieFixture(t)
			tenant.svc.mollieClient = client
			fixture.beforeRequest = func() { assertSubscriptionUnlockedDuringProviderCall(t, db, tenant.id) }
			id := uuid.NewString()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			// Shipped first payments have customer bindings, but no mandate or activation columns.
			exec(`INSERT INTO purser.payment_provider_intents (
 id, tenant_id, provider, purpose, status, currency, amount_cents,
 idempotency_key, provider_customer_id, provider_payment_id, created_at, succeeded_at
) VALUES($1::uuid,$2,'mollie','mollie_first_payment','succeeded','EUR',2000,$1::text,'customer','initial',$3,$3)`, id, tenant.id, now.Add(-time.Hour))
			if kind != "no subscription" {
				exec(`INSERT INTO purser.payment_provider_intents (
 tenant_id, provider, purpose, status, currency, idempotency_key,
 provider_customer_id, provider_subscription_id, created_at
) VALUES($1,'mollie','mollie_subscription_create','provider_open','EUR','native','customer','historical',$2)`, tenant.id, now.Add(-time.Minute))
			}
			if strings.HasPrefix(kind, "delivered") {
				exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='historical',payment_method='mollie' WHERE tenant_id=$1`, tenant.id)
			}
			fixture.route("GET", "/v2/payments/initial", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "initial", "customerId": "customer", "mandateId": "mandate", "status": "paid", "sequenceType": "first", "createdAt": now.Add(-time.Hour).Format(time.RFC3339), "paidAt": now.Add(-time.Hour).Format(time.RFC3339), "amount": map[string]string{"value": "20.00", "currency": "EUR"}}
			})
			changed := false
			fixture.route("GET", "/v2/customers/customer/subscriptions", func(recordedProviderRequest) (int, any) {
				if !changed {
					changed = true
					if kind == "delivered with bookkeeping write" {
						exec(`UPDATE purser.tenant_subscriptions
 SET updated_at=NOW(), billing_email='contact@example.test' WHERE tenant_id=$1`, tenant.id)
					}
					if kind == "delivered with concurrent subscription change" {
						exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='replacement' WHERE tenant_id=$1`, tenant.id)
					}
				}
				subscriptions := []any{}
				if kind != "no subscription" {
					subscriptions = append(subscriptions, map[string]any{"id": "historical", "mandateId": "mandate", "createdAt": now.Add(-time.Minute).Format(time.RFC3339), "status": "active"})
				}
				return 200, map[string]any{"_embedded": map[string]any{"subscriptions": subscriptions}, "_links": map[string]any{}}
			})
			q := purserdb.New(db)
			if err := q.EnqueueUnresolvedCheckoutRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Isolate the historical review from ordinary expired-checkout discovery.
			exec(`UPDATE purser.provider_webhook_inbox
 SET next_attempt_at=NOW()+INTERVAL '1 day'
 WHERE provider='checkout_recovery' AND headers->>'tenant_id'=$1`, tenant.id)
			var reviews int
			if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='checkout_delivery_review' AND headers->>'tenant_id'=$1`, tenant.id).Scan(&reviews); err != nil || reviews != 1 {
				t.Fatalf("historical discovery did not queue exactly one read-only review: count=%d err=%v", reviews, err)
			}
			pending, err := q.ListCheckoutRecovery(t.Context(), purserdb.ListCheckoutRecoveryParams{TenantID: tenant.id, RowLimit: 10})
			if err != nil {
				t.Fatal(err)
			}
			matches := 0
			for _, item := range pending {
				if item.ReservationID == id {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("pending review missing or duplicated: %+v", pending)
			}
			worker := outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.svc.logger}
			worker.ProcessBatch(t.Context())
			if kind == "delivered with concurrent subscription change" {
				var activated sql.NullTime
				var outcome sql.NullString
				if err := db.QueryRowContext(t.Context(), `SELECT activated_at,recovery_outcome FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&activated, &outcome); err != nil || activated.Valid || outcome.Valid {
					t.Fatalf("stale history inspection changed delivery: activated=%v outcome=%v err=%v", activated, outcome, err)
				}
				var state string
				if err := db.QueryRowContext(t.Context(), `SELECT status FROM purser.provider_webhook_inbox WHERE provider='checkout_delivery_review' AND headers->>'tenant_id'=$1`, tenant.id).Scan(&state); err != nil || state != "pending" {
					t.Fatalf("changed delivery was not deferred: %s %v", state, err)
				}
				exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='historical' WHERE tenant_id=$1`, tenant.id)
				exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE provider='checkout_delivery_review' AND headers->>'tenant_id'=$1`, tenant.id)
				worker.ProcessBatch(t.Context())
			}
			var activated, recovering sql.NullTime
			var outcome sql.NullString
			if err := db.QueryRowContext(t.Context(), `SELECT activated_at,recovery_started_at,recovery_outcome FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&activated, &recovering, &outcome); err != nil {
				t.Fatal(err)
			}
			if activated.Valid != strings.HasPrefix(kind, "delivered") || recovering.Valid || !strings.HasPrefix(kind, "delivered") && outcome.String != "ambiguous_delivery" {
				t.Fatalf("incorrect delivery-only result: activated=%v recovering=%v outcome=%v", activated, recovering, outcome)
			}
			items, err := q.ListCheckoutRecovery(t.Context(), purserdb.ListCheckoutRecoveryParams{TenantID: tenant.id, RowLimit: 10})
			if err != nil {
				t.Fatal(err)
			}
			matches = 0
			for _, item := range items {
				if item.ReservationID == id {
					matches++
					if item.Status != "delivery_review" {
						t.Fatalf("unexpected unresolved receipt: %+v", item)
					}
				}
			}
			want := 1
			if strings.HasPrefix(kind, "delivered") {
				want = 0
			}
			if matches != want {
				t.Fatalf("delivered payment remained or unproven payment disappeared: matches=%d want=%d inventory=%+v", matches, want, items)
			}
			if err := q.EnqueueUnresolvedCheckoutRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			worker.ProcessBatch(t.Context())
			fixture.mu.Lock()
			for _, request := range fixture.requests {
				if request.method != "GET" {
					t.Errorf("automatic delivery review mutated provider: %+v", request)
				}
			}
			fixture.mu.Unlock()
			var state string
			if err := db.QueryRowContext(t.Context(), `SELECT status FROM purser.provider_webhook_inbox WHERE provider='checkout_delivery_review' AND headers->>'tenant_id'=$1`, tenant.id).Scan(&state); err != nil || state != "processed" {
				t.Fatalf("delivery review did not finish: %s %v", state, err)
			}
		})
	}
}
