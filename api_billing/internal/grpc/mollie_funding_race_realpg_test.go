//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingmollie "frameworks/api_billing/internal/mollie"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fundingRaceMollieClient struct {
	fakeMollieBillingClient
	calls    int
	onCreate func()
}

func (c *fundingRaceMollieClient) CreateSubscription(_ context.Context, _ billingmollie.SubscriptionParams) (*mollielib.Subscription, error) {
	c.calls++
	if c.onCreate != nil {
		c.onCreate()
	}
	return c.subscription, nil
}

func assertMollieFundingRecoveryOrdersWithActivation(t *testing.T) {
	t.Helper()
	t.Run("activation refusal", assertMollieActivationRefusalFunding)
	t.Run("retained funding retry", assertMollieRetainedFundingRetry)
	t.Run("retained funding controls", assertMollieRetainedFundingControls)
	t.Run("funding purchase identity", assertMollieFundingPurchaseIdentity)
	t.Run("returning customers", assertMollieReturningCustomerFunding)
	for _, when := range []string{"recovery owns funding first", "native create owns funding first", "recovery owns native first"} {
		t.Run(when, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, paid, _ := seedFallbackLifecycle(t, db)
			if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_at=NULL,free_fallback_provider_ended_at=NULL,status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant); err != nil {
				t.Fatal(err)
			}
			q := purserdb.New(db)
			if err := q.UpsertMollieCustomer(t.Context(), purserdb.UpsertMollieCustomerParams{TenantID: tenant, MollieCustomerID: "customer"}); err != nil {
				t.Fatal(err)
			}
			funding := uuid.NewString()
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','initial','mandate',NOW(),NOW()+INTERVAL '1 hour')`, funding, tenant, paid); err != nil {
				t.Fatal(err)
			}
			service := handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)
			recoverFunding := func() {
				response, err := service.RecoverCheckoutResult(operatorAssignCtx(uuid.NewString()), tenant, funding, "recover unactivated initial payment")
				if err != nil || response.Outcome != "pending" {
					t.Fatalf("funding recovery=%+v err=%v", response, err)
				}
				if when == "native create owns funding first" && !strings.Contains(response.Detail, "linked native") {
					t.Fatalf("native funding not held: %+v", response)
				}
			}
			client := &fundingRaceMollieClient{fakeMollieBillingClient: fakeMollieBillingClient{subscription: &mollielib.Subscription{ID: "native", Status: "active"}}}
			if when == "recovery owns funding first" {
				recoverFunding()
			} else if when == "recovery owns native first" {
				client.onCreate = func() {
					var nativeID string
					if err := db.QueryRow(`SELECT id FROM purser.payment_provider_intents WHERE tenant_id=$1 AND purpose='mollie_subscription_create'`, tenant).Scan(&nativeID); err != nil {
						t.Fatal(err)
					}
					if err := q.StartCheckoutCompensation(t.Context(), purserdb.StartCheckoutCompensationParams{TenantID: tenant, IntentID: nativeID}); err != nil {
						t.Fatal(err)
					}
					if err := q.RequestCheckoutRecovery(t.Context(), purserdb.RequestCheckoutRecoveryParams{TenantID: tenant, IntentID: nativeID, Reason: "concurrent native recovery"}); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				client.onCreate = recoverFunding
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			_, err := server.CreateMollieSubscription(t.Context(), &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: paid, MandateId: "mandate"})
			if when == "recovery owns funding first" {
				if status.Code(err) != codes.FailedPrecondition || client.calls != 0 {
					t.Fatalf("compensating funding created a new subscription: calls=%d err=%v", client.calls, err)
				}
			} else if when == "recovery owns native first" {
				if status.Code(err) != codes.FailedPrecondition || client.calls != 1 || len(client.cancelledSubscriptions) != 0 {
					t.Fatalf("durable native recovery gained an inline cancellation owner: creates=%d cancels=%v err=%v", client.calls, client.cancelledSubscriptions, err)
				}
				var pending int
				if err := db.QueryRow(`SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND headers->>'tenant_id'=$1 AND status='pending'`, tenant).Scan(&pending); err != nil || pending == 0 {
					t.Fatalf("native cancellation lost durable owner: pending=%d err=%v", pending, err)
				}
			} else {
				if err != nil || client.calls != 1 {
					t.Fatalf("native activation failed: calls=%d err=%v", client.calls, err)
				}
				var activated, recovering sql.NullTime
				if err = db.QueryRowContext(t.Context(), `SELECT activated_at,recovery_started_at FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant, funding).Scan(&activated, &recovering); err != nil {
					t.Fatal(err)
				}
				if !activated.Valid || recovering.Valid {
					t.Fatalf("native activation used refundable funding: activated=%v recovering=%v", activated, recovering)
				}
			}
		})
	}
}

func assertMollieActivationRefusalFunding(t *testing.T) {
	for _, cause := range []string{"pending fallback", "pending upgrade", "expired checkout", "tier change", "other subscription", "tier retired"} {
		t.Run(cause, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, paid, _ := seedFallbackLifecycle(t, db)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_at=NULL,free_fallback_provider_ended_at=NULL,status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant)
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant)
			funding := uuid.NewString()
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','initial','mandate',NOW(),NOW()+INTERVAL '1 hour')`, funding, tenant, paid)
			client := &fundingRaceMollieClient{fakeMollieBillingClient: fakeMollieBillingClient{subscription: &mollielib.Subscription{ID: "native", Status: "active"}}}
			client.onCreate = func() {
				switch cause {
				case "pending fallback":
					exec(`UPDATE purser.tenant_subscriptions SET free_fallback_provider_stopping=true WHERE tenant_id=$1`, tenant)
				case "pending upgrade":
					exec(`UPDATE purser.tenant_subscriptions SET pending_reason='tier_upgrade' WHERE tenant_id=$1`, tenant)
				case "expired checkout":
					exec(`UPDATE purser.payment_provider_intents SET expires_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND purpose='mollie_subscription_create'`, tenant)
				case "tier change":
					other := uuid.NewString()
					exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price) VALUES($1::uuid,$1::text,'Other paid',1,20)`, other)
					exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,billing_model='postpaid',billing_period_start=NOW()-INTERVAL '1 hour',billing_period_end=NOW()+INTERVAL '1 month' WHERE tenant_id=$1`, tenant, other)
				case "other subscription":
					exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='established',stripe_subscription_status='active' WHERE tenant_id=$1`, tenant)
				case "tier retired":
					exec(`UPDATE purser.billing_tiers SET is_active=false WHERE id=$1`, paid)
				}
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			_, err := server.CreateMollieSubscription(t.Context(), &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: paid, MandateId: "mandate"})
			if status.Code(err) != codes.FailedPrecondition || client.calls != 1 || len(client.cancelledSubscriptions) != 0 {
				t.Fatalf("refusal lost durable native cancellation: creates=%d cancels=%v err=%v", client.calls, client.cancelledSubscriptions, err)
			}
			var recovering, activated sql.NullTime
			var statusText string
			var work, nativeWork int
			if err := db.QueryRow(`SELECT status,recovery_started_at,activated_at,(SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key='checkout-expiry:'||$2::text),(SELECT count(*) FROM purser.provider_webhook_inbox i JOIN purser.payment_provider_intents n ON i.event_key='checkout-expiry:'||n.id::text WHERE i.provider='checkout_recovery' AND n.tenant_id=$1 AND n.purpose='mollie_subscription_create' AND n.provider_subscription_id='native') FROM purser.payment_provider_intents WHERE tenant_id=$1::uuid AND id=$2::uuid`, tenant, funding).Scan(&statusText, &recovering, &activated, &work, &nativeWork); err != nil {
				t.Fatal(err)
			}
			definitive := cause == "tier retired"
			wantFundingWork := 0
			if definitive {
				wantFundingWork = 1
			}
			if recovering.Valid != definitive || work != wantFundingWork || activated.Valid || statusText != "succeeded" || nativeWork != 1 {
				t.Fatalf("funding disposition definitive=%v: recovering=%v refund_work=%d activated=%v status=%s native_work=%d", definitive, recovering.Valid, work, activated.Valid, statusText, nativeWork)
			}
		})
	}
}
