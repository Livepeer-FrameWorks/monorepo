//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

func TestTenantClosureCancelsPendingTierUpgrade_RealPG(t *testing.T) {
	for _, name := range []string{"requested", "provider_applied", "pricing_applied", "advance_provider_applied", "stripe_future_paid", "stripe_lost_response"} {
		t.Run(name, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			start := now.Add(-15 * 24 * time.Hour)
			end := now.Add(15 * 24 * time.Hour)
			tenant := newPhaseTenant(t, db, "postpaid", start, end, 0)
			fixture, client := installStripeFixture(t)
			tenant.svc.stripeClient = client
			wireAdvanceBilling(tenant.svc, tenant.jobs)
			target := insertTierChangeTier(t, db, "50")
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.billing_tiers SET stripe_price_id_monthly='new-price',tier_level=3 WHERE id=$1`, target)
			exec(`UPDATE purser.billing_tiers SET base_price=20 WHERE id=$1`, tenant.supporterID)
			exec(`UPDATE purser.tenant_subscriptions SET payment_method='stripe',stripe_customer_id='customer',stripe_subscription_id='subscription',
billing_name='Customer',billing_address='{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1`, tenant.id)
			cancelled := false
			metadata := map[string]string{"tenant_id": tenant.id}
			providerStart, providerEnd := start, end
			resource := func() map[string]any {
				state := "active"
				if cancelled {
					state = "canceled"
				}
				price := "old-price"
				if metadata["billing_tier_upgrade_id"] != "" {
					price = "new-price"
				}
				invoiceID := "old-invoice"
				if metadata["billing_tier_upgrade_id"] != "" {
					invoiceID = "upgrade-invoice"
				}
				return map[string]any{"id": "subscription", "object": "subscription", "customer": "customer", "status": state, "metadata": metadata, "latest_invoice": map[string]any{"id": invoiceID, "status": "paid", "amount_paid": 2000},
					"items": map[string]any{"data": []any{map[string]any{"id": "item", "price": map[string]any{"id": price}, "current_period_start": providerStart.Unix(), "current_period_end": providerEnd.Unix()}}}}
			}
			fixture.route("GET", "/v1/customers/customer", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "customer", "invoice_settings": map[string]any{"default_payment_method": "pm"}}
			})
			fixture.route("POST", "/v1/payment_intents", func(r recordedProviderRequest) (int, any) {
				if r.form.Get("amount") != "5000" {
					t.Errorf("wrong replacement charge: %v", r.form)
				}
				return 200, map[string]any{"id": "upgrade-payment", "status": "processing"}
			})
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) { return 200, resource() })
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				line := map[string]any{"amount": 5000, "pricing": map[string]any{"price_details": map[string]any{"price": "new-price"}}, "period": map[string]any{"start": providerStart.Unix(), "end": providerEnd.Unix()}}
				invoice := map[string]any{"id": "upgrade-invoice", "billing_reason": "subscription_update", "parent": map[string]any{"subscription_details": map[string]any{"metadata": metadata}}, "lines": map[string]any{"data": []any{line}}}
				return 200, map[string]any{"object": "list", "data": []any{invoice}}
			})
			fixture.route("GET", "/v1/invoice_payments", func(r recordedProviderRequest) (int, any) {
				if r.query.Get("invoice") == "upgrade-invoice" {
					return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "upgrade-payment", "amount_paid": 5000, "payment": map[string]any{"type": "charge", "charge": "upgrade-charge"}}}}
				}

				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "old-payment", "payment": map[string]any{"type": "charge", "charge": "old-charge"}}}}
			})
			fixture.route("GET", "/v1/charges/old-charge", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "old-charge", "amount": 2000, "refunded": false, "disputed": false}
			})
			fixture.route("GET", "/v1/charges/upgrade-charge", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "upgrade-charge", "amount": 5000, "currency": "eur"}
			})
			fixture.route("POST", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
				if r.form.Get("charge") != "upgrade-charge" || r.form.Get("amount") != "5000" {
					t.Errorf("wrong closure compensation: %v", r.form)
				}
				return 200, map[string]any{"id": "refund", "status": "pending", "amount": 5000, "currency": "eur"}
			})
			fixture.route("GET", "/v1/prices/new-price", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "new-price", "active": true, "currency": "eur", "unit_amount": 5000, "recurring": map[string]any{"interval": "month", "interval_count": 1}}
			})
			fixture.route("POST", "/v1/subscriptions/subscription", func(r recordedProviderRequest) (int, any) {
				metadata = map[string]string{"tenant_id": tenant.id, "billing_tier_upgrade_id": r.form.Get("metadata[billing_tier_upgrade_id]"), "tier_id": target}
				providerStart = time.Now().UTC().Truncate(time.Second)
				if name == "stripe_future_paid" {
					providerStart = providerStart.Add(5 * time.Second)
				}
				providerEnd = providerStart.AddDate(0, 1, 0)
				return 200, resource()
			})
			fixture.route("DELETE", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) { cancelled = true; return 200, resource() })
			collectUpgrade := tenant.svc.chargeTierUpgradeInvoice
			if name == "pricing_applied" || name == "advance_provider_applied" {
				exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL WHERE tenant_id=$1`, tenant.id)
				exec(`UPDATE purser.tenant_subscriptions SET presentment_currency='USD' WHERE tenant_id=$1`, tenant.id)
				exec(`UPDATE purser.billing_tiers SET metering_enabled=false WHERE id=$1`, tenant.supporterID)
				exec(`INSERT INTO purser.fx_rates(currency,reference_date,units_per_eur,source) VALUES('USD',CURRENT_DATE,1.25,'ecb') ON CONFLICT DO NOTHING`)
				exec(`INSERT INTO purser.billing_invoices(tenant_id,status,amount,base_amount,due_date,base_fee_period_start,base_fee_period_end,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,finalized_at)
VALUES($1,'paid',20,20,NOW(),$2,$3,2500,'USD',1.25,CURRENT_DATE,NOW())`, tenant.id, start, end)
				tenant.svc.chargeTierUpgradeInvoice = func(_ context.Context, _, _ string, _ time.Time) error {
					return errors.New("collection interrupted before provider call")
				}
			}
			var id string
			if err := database.WithRetryablePostgresTx(t.Context(), db, nil, func(tx *sql.Tx) error {
				if _, err := subscriptions.LockChange(t.Context(), tx, tenant.id, subscriptions.ChangePricing); err != nil {
					return err
				}
				var err error
				id, err = tenant.svc.StageTierUpgradeTx(t.Context(), tx, tenant.id, target, start, end)
				return err
			}); err != nil {
				t.Fatal(err)
			}

			if name == "advance_provider_applied" {
				row, err := purserdb.New(db).GetTierUpgrade(t.Context(), purserdb.GetTierUpgradeParams{TenantID: tenant.id, ID: id})
				if err != nil {
					t.Fatal(err)
				}
				receipt := billing.TierUpgradeReceipt{Applied: true, PeriodStart: row.RequestedAt, PeriodEnd: row.RequestedAt.AddDate(0, 1, 0)}
				encoded, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				exec(`UPDATE purser.subscription_tier_upgrades SET state='provider_applied',provider_receipt=$3 WHERE tenant_id=$1 AND id=$2`, tenant.id, id, encoded)
			}
			if name == "provider_applied" || name == "stripe_future_paid" || name == "stripe_lost_response" {
				receipt, err := client.UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: id, TenantID: tenant.id, TierID: target, CustomerID: "customer", SubscriptionID: "subscription", PriceID: "new-price", BaseCents: 5000})
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if name != "stripe_lost_response" {
					exec(`UPDATE purser.subscription_tier_upgrades SET state='provider_applied',provider_receipt=$3 WHERE tenant_id=$1 AND id=$2`, tenant.id, id, encoded)
				}
			}
			if name == "pricing_applied" {
				if ok, err := tenant.svc.ProcessTierUpgrade(t.Context(), tenant.id, id); err == nil || ok {
					t.Fatalf("interrupted collection did not remain pending: %v %v", ok, err)
				}
				tenant.svc.chargeTierUpgradeInvoice = collectUpgrade
			}

			before := len(fixture.matching("POST", "/v1/subscriptions/subscription"))
			exec(`UPDATE purser.provider_webhook_inbox SET status='failed',attempts=2,last_error='provider unavailable',next_attempt_at=NOW()+INTERVAL '1 hour' WHERE provider='tier_upgrade' AND event_key=$1`, "tier-upgrade:"+id)
			closedAt := time.Now().UTC()
			event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(closedAt))
			if err != nil {
				t.Fatal(err)
			}
			key, headers, body, err := events.EncodeRecord(event.Envelope())
			if err != nil {
				t.Fatal(err)
			}
			msg := kafka.Message{Key: key, Value: body, Topic: topology.TopicDomainEvents, Headers: map[string]string{}}
			for _, header := range headers {
				msg.Headers[header.Key] = string(header.Value)
			}
			if err = tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
				t.Fatal(err)
			}
			exec(`INSERT INTO purser.metering_sources (source_id,region,active_from,required) VALUES ('upgrade-closure','',$1,true)`, start)
			exec(`INSERT INTO purser.metering_windows (source_id,period_start,period_end,complete)
SELECT 'upgrade-closure',w,w+INTERVAL '5 minutes',true FROM generate_series($1::timestamptz,$2::timestamptz,INTERVAL '5 minutes') w`, start.Truncate(5*time.Minute), closedAt.Truncate(5*time.Minute).Add(-5*time.Minute))
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			for range 3 {
				worker.ProcessBatch(t.Context())
			}
			row, err := purserdb.New(db).GetTierUpgrade(t.Context(), purserdb.GetTierUpgradeParams{TenantID: tenant.id, ID: id})
			wantState := "cancelled"
			if name != "requested" && name != "advance_provider_applied" && name != "stripe_future_paid" {
				wantState = "completed"
			}
			if err != nil || row.State != wantState {
				t.Fatalf("closure left an upgrade outstanding: %+v %v", row, err)
			}
			closure, err := purserdb.New(db).GetTenantBillingClosure(t.Context(), tenant.id)
			if err != nil || !closure.FinalizedAt.Valid || (name != "pricing_applied" && name != "advance_provider_applied" && !cancelled) {
				t.Fatalf("closure did not finish after provider cancellation: %+v %v", closure, err)
			}
			if len(fixture.matching("POST", "/v1/subscriptions/subscription")) != before || len(fixture.matching("DELETE", "/v1/subscriptions/subscription")) != func() int {
				if name == "pricing_applied" || name == "advance_provider_applied" {
					return 0
				}
				return 1
			}() || len(fixture.matching("POST", "/v1/refunds")) != func() int {
				if name == "stripe_future_paid" {
					return 1
				}
				return 0
			}() {
				t.Fatal("closure repriced or refunded the subscription")
			}
			var tier string
			var newFees int
			wantTier := tenant.supporterID
			if name != "requested" && name != "advance_provider_applied" && name != "stripe_future_paid" {
				wantTier = target
			}
			if err = db.QueryRowContext(t.Context(), `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&tier); err != nil || tier != wantTier {
				t.Fatalf("closed account installed its pending tier: %s %v", tier, err)
			}
			wantFees := 0
			if name == "advance_provider_applied" {
				wantFees = 1
			}
			if name == "pricing_applied" {
				wantFees = 2
			}
			if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=$1 AND base_fee_period_start IS NOT NULL`, tenant.id).Scan(&newFees); err != nil || newFees != wantFees {
				t.Fatalf("closure created a replacement fee: %d %v", newFees, err)
			}
			if name == "pricing_applied" {
				var payments int
				if e := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.billing_payments p JOIN purser.billing_invoices i ON i.id=p.invoice_id WHERE i.tenant_id=$1 AND i.base_fee_period_start IS NOT NULL`, tenant.id).Scan(&payments); e != nil || payments != 1 {
					t.Fatalf("closure skipped durable upgrade collection: payments=%d err=%v", payments, e)
				}
				if count := len(fixture.matching("POST", "/v1/payment_intents")); count != 1 {
					t.Fatalf("upgrade charge count=%d want 1", count)
				}
			}
			failed, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(t.Context())
			if err != nil || failed != 0 {
				t.Fatalf("closed account retained a failed upgrade alert: count=%d err=%v", failed, err)
			}
		})
	}
}
