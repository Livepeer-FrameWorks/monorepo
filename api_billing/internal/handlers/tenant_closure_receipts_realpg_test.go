//go:build schema_verify

package handlers

import (
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestTenantClosureRecoversUnfulfilledClusterReceipts_RealPG(t *testing.T) {
	for _, mode := range []string{"open checkout", "response after empty recovery", "unfulfilled paid checkout", "established subscription"} {
		t.Run(mode, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.ExecContext(t.Context(), `UPDATE purser.billing_tiers SET metering_enabled=false WHERE id IN ($1,$2)`, tenant.supporterID, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			q := purserdb.New(db)
			id, err := q.UpsertStripeClusterCheckoutIntent(t.Context(), purserdb.UpsertStripeClusterCheckoutIntentParams{TenantID: tenant.id, Currency: "EUR", IdempotencyKey: "cluster-receipt:" + tenant.id})
			if err != nil {
				t.Fatal(err)
			}
			if err := q.LinkProviderIntentCustomer(t.Context(), purserdb.LinkProviderIntentCustomerParams{TenantID: tenant.id, IntentID: id, CustomerID: sql.NullString{String: "customer", Valid: true}}); err != nil {
				t.Fatal(err)
			}
			bind := func() {
				t.Helper()
				if err := q.SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: id, SessionID: sql.NullString{String: "session", Valid: true}}); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "response after empty recovery" {
				bind()
			}
			fixture, client := installStripeFixture(t)
			tenant.svc.stripeClient = client
			var expired, cancelled, refunded atomic.Bool
			fixture.route("GET", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
				if mode == "unfulfilled paid checkout" {
					return 200, map[string]any{"id": "session", "status": "complete", "payment_status": "paid", "subscription": "subscription"}
				}
				state := "open"
				if expired.Load() {
					state = "expired"
				}
				return 200, map[string]any{"id": "session", "status": state}
			})
			fixture.route("POST", "/v1/checkout/sessions/session/expire", func(recordedProviderRequest) (int, any) {
				expired.Store(true)
				return 200, map[string]any{"id": "session", "status": "expired"}
			})
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				state := "active"
				if cancelled.Load() {
					state = "canceled"
				}
				return 200, map[string]any{"id": "subscription", "status": state}
			})
			fixture.route("DELETE", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				cancelled.Store(true)
				return 200, map[string]any{"id": "subscription", "status": "canceled"}
			})
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "invoice", "billing_reason": "subscription_create", "amount_paid": 2900}}, "has_more": false}
			})
			fixture.route("GET", "/v1/invoice_payments", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "payment", "payment": map[string]any{"type": "charge", "charge": "charge"}}}, "has_more": false}
			})
			fixture.route("GET", "/v1/charges/charge", func(recordedProviderRequest) (int, any) {
				amount := 0
				if refunded.Load() {
					amount = 2900
				}
				return 200, map[string]any{"id": "charge", "currency": "eur", "amount": 2900, "amount_refunded": amount, "refunded": refunded.Load()}
			})
			fixture.route("GET", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
				if r.query.Get("charge") == "" {
					t.Error("refund history was not scoped to the purchase charge")
				}
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			fixture.route("POST", "/v1/refunds", func(recordedProviderRequest) (int, any) {
				refunded.Store(true)
				return 200, map[string]any{"id": "refund", "status": "pending"}
			})
			if mode == "unfulfilled paid checkout" {
				if err := tenant.svc.stageClusterSubscriptionPending(t.Context(), "session", tenant.id, "marketplace", "customer", "subscription"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "established subscription" {
				if err := tenant.svc.activateClusterSubscriptionFromStripe(t.Context(), tenant.id, "marketplace", "customer", "subscription", "session"); err != nil {
					t.Fatal(err)
				}
				// Installed access is completed service even when its receipt
				// still says provider_open.
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET status='provider_open' WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			}
			deliverAccountClosureForTest(t, tenant.jobs, tenant.id, now)
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			for range 3 {
				worker.ProcessBatch(t.Context())
			}
			if mode == "established subscription" {
				var compensation int
				if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+id).Scan(&compensation); err != nil || compensation != 0 || !cancelled.Load() {
					t.Fatalf("completed service was treated as unfulfilled checkout: recovery=%d cancelled=%v err=%v", compensation, cancelled.Load(), err)
				}
				return
			}
			row, err := q.GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
			if err != nil || row.Status != "expired" || !row.RecoveryStartedAt.Valid {
				t.Fatalf("account closure left a payable cluster receipt: %+v %v", row, err)
			}
			if mode == "response after empty recovery" {
				bind()
				for range 3 {
					worker.ProcessBatch(t.Context())
				}
			}
			if mode == "unfulfilled paid checkout" {
				if !refunded.Load() || !cancelled.Load() || len(fixture.matching("POST", "/v1/refunds")) != 1 || len(fixture.matching("DELETE", "/v1/subscriptions/subscription")) != 1 {
					t.Fatal("account closure must refund only the never-activated cluster purchase")
				}
				if row.RecoveryOutcome.String != "cancelled_refund_requested" {
					t.Fatalf("closure reported the wrong provider action: %s", row.RecoveryOutcome.String)
				}
				return
			}
			if !expired.Load() || len(fixture.matching("POST", "/v1/checkout/sessions/session/expire")) != 1 {
				t.Fatal("cluster checkout stayed payable after account closure or was expired twice")
			}
		})
	}
}

func deliverAccountClosureForTest(t *testing.T, jobs *JobManager, tenantID string, at time.Time) {
	t.Helper()
	event, err := events.New("commodore", tenantID, tenantID, &internalv1.TenantAccountClosed{}, events.WithTime(at))
	if err != nil {
		t.Fatal(err)
	}
	key, headers, body, err := events.EncodeRecord(event.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	msg := kafka.Message{Key: key, Value: body, Topic: topology.TopicDomainEvents, Headers: map[string]string{}}
	for _, h := range headers {
		msg.Headers[h.Key] = string(h.Value)
	}
	if err := jobs.handleBillingMessage(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
}
