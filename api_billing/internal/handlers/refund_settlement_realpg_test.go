//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
)

type refundCreditQuartermaster struct {
	phaseQuartermaster
	owner string
}

func (q refundCreditQuartermaster) GetCluster(_ context.Context, id string) (*quartermasterpb.ClusterResponse, error) {
	return &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{ClusterId: id, OwnerTenantId: &q.owner}}, nil
}

func TestCheckoutRefundSettlementAndOperatorCredit_RealPG(t *testing.T) {
	for _, mode := range []string{"accrual before compensation", "accrual after settlement", "failed bank return"} {
		t.Run(mode, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			ctx := t.Context()
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			fixture, client := installStripeFixture(t)
			tenant.svc.stripeClient = client
			tenant.svc.qmClient = refundCreditQuartermaster{owner: uuid.NewString()}
			id := uuid.NewString()
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_session_id,provider_subscription_id,recovery_started_at,expires_at)
 VALUES($1::uuid,$2,'stripe','cluster_subscription_checkout','provider_open','EUR',$1::text,'session','subscription',NOW(),NOW()-INTERVAL '1 hour')`, id, tenant.id); err != nil {
				t.Fatal(err)
			}
			if err := tenant.svc.stageClusterSubscriptionPending(ctx, "session", tenant.id, "marketplace", "customer", "subscription"); err != nil {
				t.Fatal(err)
			}
			invoice := &StripeInvoiceObject{ID: "invoice", CustomerID: "customer", SubscriptionID: "subscription", Status: "paid", AmountPaid: 2000, Currency: "eur", PeriodStart: now.Add(-time.Hour).Unix(), PeriodEnd: now.AddDate(0, 1, 0).Unix()}
			if mode != "accrual after settlement" {
				if err := tenant.svc.recordMonthlyClusterCredit(ctx, invoice); err != nil {
					t.Fatal(err)
				}
			}
			refundRequested := false
			bankState := "pending"
			fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "session", "status": "complete", "subscription": "subscription", "payment_status": "paid"}
			})
			fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "subscription", "status": "canceled"}
			})
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "invoice", "amount_paid": 2000, "billing_reason": "subscription_create"}}}
			})
			fixture.route("GET", "/v1/invoice_payments", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "invoice-payment", "amount_paid": 2000, "payment": map[string]any{"type": "charge", "charge": "charge"}}}}
			})
			fixture.route("GET", "/v1/charges/charge", func(recordedProviderRequest) (int, any) {
				returned := 0
				if refundRequested {
					returned = 2000
				}
				return 200, map[string]any{"id": "charge", "amount": 2000, "amount_refunded": returned, "payment_intent": "pi", "currency": "eur"}
			})
			fixture.route("POST", "/v1/refunds", func(recordedProviderRequest) (int, any) {
				refundRequested = true
				return 200, map[string]any{"id": "refund", "status": "pending", "amount": 2000, "currency": "eur"}
			})
			fixture.route("GET", "/v1/refunds", func(recordedProviderRequest) (int, any) {
				if !refundRequested {
					return 200, map[string]any{"object": "list", "data": []any{}}
				}
				return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "refund", "status": bankState, "amount": 2000, "currency": "eur", "charge": "charge"}}}
			})
			if err := tenant.svc.expireCheckoutReservation(ctx, checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); err != nil {
				t.Fatal(err)
			}
			assertCredit := func() {
				t.Helper()
				var gross, payable int64
				if err := db.QueryRow(`SELECT COALESCE(SUM(gross_cents),0),COALESCE(SUM(payable_cents),0) FROM purser.operator_credit_ledger WHERE stripe_invoice_id='invoice'`).Scan(&gross, &payable); err != nil || gross != 0 || payable != 0 {
					t.Fatalf("unactivated purchase retained producer credit: gross=%d payable=%d err=%v", gross, payable, err)
				}
			}
			assertCredit()
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			poll := func() {
				t.Helper()
				if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE provider='refund_settlement'`); err != nil {
					t.Fatal(err)
				}
				worker.ProcessBatch(ctx)
			}
			poll()
			var state string
			if err := db.QueryRow(`SELECT status FROM purser.provider_webhook_inbox WHERE provider='refund_settlement'`).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("accepted refund was treated as bank settlement: state=%s err=%v", state, err)
			}
			if mode == "failed bank return" {
				bankState = "failed"
				poll()
				poll()
				failures, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(ctx)
				if err != nil || failures < 1 {
					t.Fatalf("failed bank return was silent: count=%d err=%v", failures, err)
				}
			}
			bankState = "succeeded"
			raw, err := json.Marshal(map[string]any{"id": "charge", "payment_intent": "pi", "amount": 2000, "amount_refunded": 2000, "currency": "eur", "refunds": map[string]any{"data": []any{map[string]any{"id": "refund", "amount": 2000, "currency": "eur", "status": "succeeded"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := tenant.svc.handleStripeChargeRefunded(StripeWebhookPayload{Type: "charge.refunded", Data: struct {
				Object json.RawMessage `json:"object"`
			}{Object: raw}}); err != nil {
				t.Fatalf("compensated purchase refund has no local invoice but must reconcile: %v", err)
			}
			poll()
			if err := db.QueryRow(`SELECT status FROM purser.provider_webhook_inbox WHERE provider='refund_settlement'`).Scan(&state); err != nil || state != "processed" {
				t.Fatalf("confirmed return stayed outstanding: state=%s err=%v", state, err)
			}
			if mode == "accrual after settlement" {
				if err := tenant.svc.recordMonthlyClusterCredit(ctx, invoice); err != nil {
					t.Fatal(err)
				}
			}
			assertCredit()
			if err := tenant.svc.handleStripeChargeRefunded(StripeWebhookPayload{Type: "charge.refunded", Data: struct {
				Object json.RawMessage `json:"object"`
			}{Object: raw}}); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT status FROM purser.provider_webhook_inbox WHERE provider='refund_settlement'`).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("provider replay did not recheck bank evidence: state=%s err=%v", state, err)
			}
			worker.ProcessBatch(ctx)
			var corrections, receipts int
			if err := db.QueryRow(`SELECT count(*) FROM purser.operator_credit_ledger WHERE stripe_invoice_id='invoice' AND entry_type='clawback'`).Scan(&corrections); err != nil || corrections != 1 {
				t.Fatalf("duplicate/missing operator correction: %d %v", corrections, err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM purser.provider_payment_objects WHERE tenant_id=$1 AND provider='stripe' AND object_type='refund'`, tenant.id).Scan(&receipts); err != nil || receipts != 1 {
				t.Fatalf("duplicate/missing bank receipt: %d %v", receipts, err)
			}
			if count := len(fixture.matching("POST", "/v1/refunds")); count != 1 {
				t.Fatalf("bank polling moved money: refund requests=%d", count)
			}
		})
	}
}
