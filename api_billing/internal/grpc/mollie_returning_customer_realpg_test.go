//go:build schema_verify

package grpc

import (
	"database/sql"
	"strings"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func assertMollieReturningCustomerFunding(t *testing.T) {
	for _, history := range []string{
		"v0.3.13 delivered payment", "consumed payment", "reuse before consumed",
		"reuse before refunded", "reuse before recovering", "reuse before other tier",
		"reuse before other currency", "reuse before zero setup",
	} {
		t.Run(history, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, tier, free := seedFallbackLifecycle(t, db)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='prepaid',stripe_subscription_id=NULL,stripe_customer_id=NULL,mollie_subscription_id='previous',free_fallback_at=NULL,free_fallback_provider_ended_at=NOW()-INTERVAL '1 day',status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant, free)
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant)
			funding := uuid.NewString()
			// v0.3.13 records the paid first payment, but neither of the two
			// activation-ownership columns introduced by v0.3.14 exists there.
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,succeeded_at,created_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','old-payment',NOW()-INTERVAL '2 months',NOW()-INTERVAL '2 months')`, funding, tenant, tier)
			exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_subscription_id,created_at) VALUES($1,'mollie','mollie_subscription_create','billing_tiers',$2,'provider_open','EUR',2000,$3,'customer','previous',NOW()-INTERVAL '2 months')`, tenant, tier, "mollie-subscription:"+tenant+":"+tier+":mandate")
			if history == "consumed payment" || strings.HasPrefix(history, "reuse before ") {
				exec(`UPDATE purser.payment_provider_intents SET provider_mandate_id='mandate' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			}
			if history == "consumed payment" {
				exec(`UPDATE purser.payment_provider_intents SET activated_at=NOW()-INTERVAL '2 months' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				exec(`UPDATE purser.payment_provider_intents SET status='succeeded',activated_at=NOW()-INTERVAL '2 months',funding_intent_id=$2 WHERE tenant_id=$1 AND purpose='mollie_subscription_create'`, tenant, funding)
			}
			reusable := ""
			if strings.HasPrefix(history, "reuse before ") {
				reusable = uuid.NewString()
				exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,created_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','reusable-payment','mandate',NOW()-INTERVAL '3 months',NOW()-INTERVAL '3 months')`, reusable, tenant, tier)
				switch history {
				case "reuse before consumed":
					exec(`UPDATE purser.payment_provider_intents SET activated_at=NOW()-INTERVAL '2 months' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				case "reuse before refunded":
					exec(`UPDATE purser.payment_provider_intents SET status='expired' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				case "reuse before recovering":
					exec(`UPDATE purser.payment_provider_intents SET recovery_started_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				case "reuse before other tier":
					exec(`UPDATE purser.payment_provider_intents SET local_reference_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, funding, free)
				case "reuse before other currency":
					exec(`UPDATE purser.payment_provider_intents SET currency='USD' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				case "reuse before zero setup":
					exec(`UPDATE purser.payment_provider_intents SET amount_cents=0 WHERE tenant_id=$1 AND id=$2`, tenant, funding)
				}
			}
			client := &retainedFundingMollieClient{}
			client.subscription = &mollielib.Subscription{ID: "new-subscription", Status: "active"}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			activation := &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: tier, MandateId: "mandate"}
			if reusable != "" {
				out, err := server.CreateMollieSubscription(t.Context(), activation)
				if err != nil || out.GetSubscriptionId() != "new-subscription" || client.calls != 1 || client.payments != 0 {
					t.Fatalf("unusable latest receipt hides reusable funding: creates=%d payments=%d out=%v err=%v", client.calls, client.payments, out, err)
				}
				var consumed bool
				var linked string
				if err = db.QueryRow(`SELECT p.activated_at IS NOT NULL,n.funding_intent_id::text FROM purser.payment_provider_intents p JOIN purser.payment_provider_intents n ON n.tenant_id=p.tenant_id AND n.provider_subscription_id='new-subscription' WHERE p.tenant_id=$1 AND p.id=$2`, tenant, reusable).Scan(&consumed, &linked); err != nil || !consumed || linked != reusable {
					t.Fatalf("activation did not consume selected funding: consumed=%v linked=%s err=%v", consumed, linked, err)
				}
				return
			}
			if history == "consumed payment" {
				_, err := server.CreateMollieSubscription(t.Context(), activation)
				if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "complete a new first payment") || strings.Contains(status.Convert(err).Message(), "retry activation using the existing") || client.calls != 0 {
					t.Errorf("consumed payment guidance: creates=%d err=%v", client.calls, err)
				}
			}
			payment, err := server.CreateFirstPayment(t.Context(), &purserpb.CreateMollieFirstPaymentRequest{TenantId: tenant, TierId: tier, Method: "creditcard", RedirectUrl: "https://example.test/return"})
			if err != nil || client.payments != 1 {
				t.Fatalf("returning customer cannot pay for a new month: calls=%d err=%v", client.payments, err)
			}
			if err = purserdb.New(db).ResolveMollieFirstPaymentIntent(t.Context(), purserdb.ResolveMollieFirstPaymentIntentParams{TenantID: tenant, ProviderPaymentID: sql.NullString{String: payment.PaymentId, Valid: true}, MandateID: sql.NullString{String: "mandate", Valid: true}, Status: "succeeded"}); err != nil {
				t.Fatal(err)
			}
			out, err := server.CreateMollieSubscription(t.Context(), activation)
			if err != nil || out.GetSubscriptionId() != "new-subscription" || client.calls != 1 {
				t.Fatalf("new payment cannot activate a returning subscription: creates=%d out=%v err=%v", client.calls, out, err)
			}
			var oldActivated, recovering sql.NullTime
			var paidAndActivated int
			if err = db.QueryRow(`SELECT activated_at,recovery_started_at,(SELECT count(*) FROM purser.payment_provider_intents p WHERE p.tenant_id=$1 AND p.provider_payment_id=$3 AND p.activated_at IS NOT NULL) FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant, funding, payment.PaymentId).Scan(&oldActivated, &recovering, &paidAndActivated); err != nil {
				t.Fatal(err)
			}
			if oldActivated.Valid != (history == "consumed payment") || recovering.Valid || paidAndActivated != 1 {
				t.Fatalf("new purchase modified old funding: old_activated=%v old_recovery=%v new_activated=%d", oldActivated, recovering, paidAndActivated)
			}
		})
	}
}
