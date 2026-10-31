//go:build schema_verify

package handlers

import (
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestTenantClosureStopsBilling_RealPG(t *testing.T) {
	assertClosureRefundsOnlyUnactivatedFirstPayment(t)
	for _, provider := range []string{"stripe", "mollie", "manual", "prepaid", "active_grant"} {
		t.Run(provider, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Microsecond)
			closedAt := now.Add(-time.Hour)
			model := "postpaid"
			if provider == "prepaid" {
				model = "prepaid"
			}
			tenant := newPhaseTenant(t, db, model, now.Add(-24*time.Hour), now.AddDate(0, 1, 0), 0)
			if _, err := db.Exec(`UPDATE purser.billing_tiers SET metering_enabled=false WHERE id IN ($1,$2)`, tenant.supporterID, tenant.paygID); err != nil {
				t.Fatal(err)
			}
			var fixture *providerFixture
			cancelPath := ""
			switch provider {
			case "stripe":
				fixture, tenant.svc.stripeClient = installStripeFixture(t)
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_customer_id='customer',stripe_subscription_id='subscription',payment_method='stripe' WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
				cancelPath = "/v1/subscriptions/subscription"
				fixture.route("GET", cancelPath, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "subscription", "status": "active"}
				})
				fixture.route("DELETE", cancelPath, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "subscription", "status": "canceled"}
				})
			case "mollie":
				fixture, tenant.svc.mollieClient = installMollieFixture(t)
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='subscription',payment_method='mollie' WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant.id); err != nil {
					t.Fatal(err)
				}
				cancelPath = "/v2/customers/customer/subscriptions/subscription"
				fixture.route("GET", cancelPath, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "subscription", "status": "active"}
				})
				fixture.route("DELETE", cancelPath, func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "subscription", "status": "canceled"}
				})
			}
			if provider == "active_grant" {
				if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,expires_at,reason,granted_by,fallback_tier_id,assigned_tier_id) SELECT id,0,true,'invoice',NOW()+INTERVAL '1 month','trial','operator',tier_id,tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
			}
			event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(closedAt))
			if err != nil {
				t.Fatal(err)
			}
			key, headers, body, err := events.EncodeRecord(event.Envelope())
			if err != nil {
				t.Fatal(err)
			}
			msg := kafka.Message{Key: key, Headers: map[string]string{}, Value: body, Topic: topology.TopicDomainEvents}
			for _, h := range headers {
				msg.Headers[h.Key] = string(h.Value)
			}
			if err := tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			cancellationFails := fixture != nil
			if fixture != nil {
				fixture.route("DELETE", cancelPath, func(recordedProviderRequest) (int, any) {
					if cancellationFails {
						return 503, map[string]any{"error": map[string]any{"type": "api_error", "message": "unavailable"}, "status": 503, "title": "unavailable", "detail": "temporarily unavailable"}
					}
					return 200, map[string]any{"id": "subscription", "status": "canceled"}
				})
			}
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			worker.ProcessBatch(t.Context())
			if fixture != nil {
				closure, err := purserdb.New(db).GetTenantBillingClosure(t.Context(), tenant.id)
				if err != nil || closure.FinalizedAt.Valid || closure.ProvidersCancelledAt.Valid {
					t.Fatalf("provider outage acknowledged closure: %+v %v", closure, err)
				}
				if _, err := subscriptions.TransitionProvider(t.Context(), db, subscriptions.ProviderChange{TenantID: tenant.id, Operation: subscriptions.ProviderCheckout}); !errors.Is(err, subscriptions.ErrTenantBillingClosed) {
					t.Fatalf("closed account can start checkout: %v", err)
				}
				due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(t.Context(), now.AddDate(0, 2, 0))
				if err != nil {
					t.Fatal(err)
				}
				if generated := tenant.jobs.finalizeSubscriptionPeriods(t.Context(), due, now.AddDate(0, 2, 0), time.Time{}); generated != 0 {
					t.Fatal("provider outage restarted recurring invoicing")
				}
				cancellationFails = false
				if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE provider='tenant_closure' AND headers->>'tenant_id'=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
				worker.ProcessBatch(t.Context())
			}

			if provider == "active_grant" {
				var until time.Time
				if err := db.QueryRow(`SELECT g.expires_at FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, tenant.id).Scan(&until); err != nil || !until.Equal(closedAt) {
					t.Fatalf("trial not ended at closure cutoff: until=%s err=%v", until, err)
				}
				closure, err := purserdb.New(db).GetTenantBillingClosure(t.Context(), tenant.id)
				if err != nil || closure.FinalizedAt.Valid {
					t.Fatalf("closure must await the ordinary grant-expiry boundary: %+v %v", closure, err)
				}
				var queued int
				if err := db.QueryRow(`SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='tenant_closure' AND status IN ('pending','failed') AND headers->>'tenant_id'=$1`, tenant.id).Scan(&queued); err != nil || queued != 1 {
					t.Fatalf("trial closure lost its durable retry: count=%d err=%v", queued, err)
				}
				return
			}

			var state string
			if err := db.QueryRow(`SELECT status FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&state); err != nil || state != "cancelled" {
				t.Fatalf("closed tenant is still billable: state=%s err=%v", state, err)
			}
			if fixture != nil && len(fixture.matching("DELETE", cancelPath)) != 2 {
				t.Fatalf("closure left provider subscription charging: cancellations=%d", len(fixture.matching("DELETE", cancelPath)))
			}
			var documents int
			var amount float64
			if err := db.QueryRow(`SELECT count(*),COALESCE(sum(amount),0) FROM purser.billing_invoices WHERE tenant_id=$1 AND period_end=$2 AND status<>'draft'`, tenant.id, closedAt).Scan(&documents, &amount); err != nil || documents != 1 {
				t.Fatalf("final document does not end at closure: documents=%d amount=%v err=%v", documents, amount, err)
			}
			if provider == "manual" && amount <= 0 {
				t.Fatalf("final invoice omitted the positive prorated base fee: %v", amount)
			}
			if err := tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			worker.ProcessBatch(t.Context())
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1`, tenant.id).Scan(&count); err != nil || count != documents {
				t.Fatalf("redelivered closure invoiced twice: %d %v", count, err)
			}
			if fixture != nil && len(fixture.matching("DELETE", cancelPath)) != 2 {
				t.Fatal("redelivered closure repeated provider cancellation")
			}
			due, err := purserdb.New(db).ListSubscriptionsDueForInvoice(t.Context(), now.AddDate(0, 2, 0))
			if err != nil {
				t.Fatal(err)
			}
			if generated := tenant.jobs.finalizeSubscriptionPeriods(t.Context(), due, now.AddDate(0, 2, 0), time.Time{}); generated != 0 {
				t.Fatalf("closed tenant invoiced again: %d", generated)
			}
		})
	}
}
