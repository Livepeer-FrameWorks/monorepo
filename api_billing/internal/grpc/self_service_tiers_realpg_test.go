//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	stripelib "github.com/stripe/stripe-go/v85"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Existing checkouts must recheck catalog eligibility when their provider completes.
func assertSelfServiceCheckoutEligibility(t *testing.T, db *sql.DB) {
	t.Helper()
	t.Run("Mollie first-payment refusal", func(t *testing.T) { assertMollieFirstPaymentRefusalQueuesRecovery(t, db) })
	t.Run("Mollie refused activation queues linked funding", func(t *testing.T) { assertRefusedMollieActivationQueuesFunding(t, db) })
	for _, mode := range []string{"enterprise", "inactive", "zero level", "paid advance", "past due advance", "native Stripe", "native Mollie"} {
		t.Run("checkout eligibility/"+mode, func(t *testing.T) {
			tenant, current, target := uuid.NewString(), uuid.NewString(), uuid.NewString()
			ctx := t.Context()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,is_enterprise,is_active,stripe_price_id_monthly)
VALUES($1::uuid,$1::text,'Current',4,50,false,true,'price_current'),($2::uuid,$2::text,'Target',2,20,$3,$4,'price_target')`, current, target, mode == "enterprise", mode != "inactive")
			if mode == "zero level" {
				exec(`UPDATE purser.billing_tiers SET tier_level=0 WHERE id=$1`, target)
			}
			exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,
billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end)
VALUES($1,$2,'active','postpaid','stripe','cus_eligibility','Customer','billing@example.com',
'{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}','USD',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days')`, tenant, current)
			if mode == "past due advance" {
				exec(`UPDATE purser.tenant_subscriptions SET status='past_due' WHERE tenant_id=$1`, tenant)
			}
			if mode == "native Stripe" || mode == "native Mollie" {
				exec(`UPDATE purser.tenant_subscriptions SET presentment_currency='EUR' WHERE tenant_id=$1`, tenant)
			}

			if mode == "native Stripe" {
				exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='native_existing',stripe_subscription_status='active' WHERE tenant_id=$1`, tenant)
			}
			if mode == "native Mollie" {
				exec(`UPDATE purser.tenant_subscriptions SET payment_method='mollie',mollie_subscription_id='native_existing' WHERE tenant_id=$1`, tenant)
			}

			stripeClient := &fakeStripeBillingClient{customer: &stripelib.Customer{ID: "cus_eligibility"}, session: &stripelib.CheckoutSession{ID: "cs_ineligible", URL: "https://example.test/checkout"}}
			mollieClient := &eligibilityMollieClient{}
			server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: stripeClient, mollieClient: mollieClient, commodoreClient: &fakeCommodorePrimaryUser{}}
			_, err := server.CreateCheckoutSession(ctx, &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: target, BillingPeriod: "monthly", SuccessUrl: "https://example.test/success", CancelUrl: "https://example.test/cancel"})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("Stripe checkout accepted ineligible purchase: %v", err)
			}
			_, err = server.CreateFirstPayment(ctx, &purserpb.CreateMollieFirstPaymentRequest{TenantId: tenant, TierId: target, Method: "creditcard", RedirectUrl: "https://example.test/return"})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("Mollie checkout accepted ineligible purchase: %v", err)
			}
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,$2)`, tenant, "customer-"+tenant)
			if mode != "native Stripe" && mode != "native Mollie" {
				exec(`UPDATE purser.tenant_subscriptions SET presentment_currency='EUR' WHERE tenant_id=$1`, tenant)
			}
			_, err = server.CreateMollieSubscription(ctx, &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: target, MandateId: "mandate"})
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("Mollie native checkout accepted ineligible purchase: %v", err)
			}
			if mode != "native Stripe" && mode != "native Mollie" {
				exec(`UPDATE purser.tenant_subscriptions SET presentment_currency='USD' WHERE tenant_id=$1`, tenant)
			}

			if stripeClient.checkoutKey != "" || mollieClient.calls != 0 {
				t.Fatalf("ineligible checkout reached provider: stripe=%s mollie=%d", stripeClient.checkoutKey, mollieClient.calls)
			}
			if mode == "enterprise" {
				_, err = server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: target})
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("Enterprise downgrade accepted: %v", err)
				}
				exec(`UPDATE purser.tenant_subscriptions SET billing_model='prepaid' WHERE tenant_id=$1`, tenant)
				_, err = server.PromoteToPaid(ctx, &purserpb.PromoteToPaidRequest{TenantId: tenant, TierId: target})
				if status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("Enterprise promotion accepted: %v", err)
				}
				exec(`UPDATE purser.tenant_subscriptions SET billing_model='postpaid' WHERE tenant_id=$1`, tenant)
			}
			for _, operation := range []subscriptions.ProviderOperation{subscriptions.StripeCheckout, subscriptions.StripePending, subscriptions.MollieActivation, subscriptions.AdvanceActivation} {
				change := subscriptions.ProviderChange{TenantID: tenant, Operation: operation,
					Stripe:  purserdb.ActivateTenantSubscriptionFromStripeParams{TierID: target, CustomerID: "cus_eligibility", SubscriptionID: "sub_ineligible"},
					Mollie:  purserdb.ActivateMollieTenantSubscriptionParams{TierID: target, SubscriptionID: sql.NullString{String: "sub_ineligible", Valid: true}},
					Advance: purserdb.ActivateAdvanceBilledSubscriptionParams{TierID: target, PaymentMethod: "mollie", PeriodStart: time.Now(), PeriodEnd: time.Now().Add(30 * 24 * time.Hour)}}
				_, err = subscriptions.TransitionProvider(ctx, db, change)
				if !errors.Is(err, subscriptions.ErrCheckoutExpired) {
					t.Fatalf("open checkout operation %d bypassed eligibility or cannot compensate: %v", operation, err)
				}
			}
			if mode == "native Stripe" || mode == "native Mollie" {
				operation := subscriptions.StripeCheckout
				if mode == "native Mollie" {
					operation = subscriptions.MollieActivation
				}
				result, replayErr := subscriptions.TransitionProvider(ctx, db, subscriptions.ProviderChange{TenantID: tenant, Operation: operation,
					Stripe: purserdb.ActivateTenantSubscriptionFromStripeParams{TierID: current, CustomerID: "cus_eligibility", SubscriptionID: "native_existing"},
					Mollie: purserdb.ActivateMollieTenantSubscriptionParams{TierID: current, SubscriptionID: sql.NullString{String: "native_existing", Valid: true}, NextPaymentDate: time.Now().Add(30 * 24 * time.Hour)}})
				if replayErr != nil || result.Rows != 1 {
					t.Fatalf("matching native subscription replay refused: %+v %v", result, replayErr)
				}
			}

			var actual string
			if err = db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&actual); err != nil || actual != current {
				t.Fatalf("refused checkout changed tier: %s %v", actual, err)
			}
		})
	}
}

type eligibilityMollieClient struct {
	fakeMollieBillingClient
	calls int
}

func (f *eligibilityMollieClient) CreateOrGetCustomer(context.Context, billingmollie.CustomerInfo) (*mollielib.Customer, error) {
	f.calls++
	return nil, errors.New("ineligible checkout reached Mollie customer")
}
func (f *eligibilityMollieClient) CreateFirstPayment(context.Context, billingmollie.FirstPaymentParams) (*mollielib.Payment, error) {
	f.calls++
	return nil, errors.New("ineligible checkout reached Mollie payment")
}

func (f *eligibilityMollieClient) GetMandate(context.Context, string, string) (*mollielib.Mandate, error) {
	f.calls++
	return &mollielib.Mandate{ID: "mandate", Status: "valid"}, nil
}
func (f *eligibilityMollieClient) CreateSubscription(context.Context, billingmollie.SubscriptionParams) (*mollielib.Subscription, error) {
	f.calls++
	return nil, errors.New("ineligible checkout reached Mollie subscription")
}

func assertRefusedMollieActivationQueuesFunding(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tenant, current, target, funding, native := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,is_enterprise) VALUES($1::uuid,$1::text,'Current',1,0,false),($2::uuid,$2::text,'Enterprise',3,20,true)`, []any{current, target}},
		{`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,payment_method,presentment_currency) VALUES($1,$2,'active','prepaid','mollie','EUR')`, []any{tenant, current}},
		{`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','paid-first','mandate',NOW(),NOW()+INTERVAL '1 hour')`, []any{funding, tenant, target}},
		{`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_subscription_id,funding_intent_id,expires_at) VALUES($1::uuid,$2,'mollie','mollie_subscription_create','billing_tiers',$3,'provider_open','EUR',2000,$1::text,'customer','refused-native',$4,NOW()+INTERVAL '1 hour')`, []any{native, tenant, target, funding}},
	} {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	rows, err := server.activateMollieSubscription(ctx, purserdb.ActivateMollieTenantSubscriptionParams{TenantID: tenant, TierID: target, SubscriptionID: sql.NullString{String: "refused-native", Valid: true}, NextPaymentDate: time.Now().Add(30 * 24 * time.Hour)}, native)
	if rows != 0 || !errors.Is(err, subscriptions.ErrCheckoutExpired) {
		t.Fatalf("native refusal classification: rows=%d err=%v", rows, err)
	}
	for _, id := range []string{native, funding} {
		var latched, forced bool
		if err := db.QueryRowContext(ctx, `SELECT p.recovery_started_at IS NOT NULL,convert_from(i.raw_payload,'UTF8')::jsonb->>'force'='true' FROM purser.payment_provider_intents p JOIN purser.provider_webhook_inbox i ON i.provider='checkout_recovery' AND i.event_key='checkout-expiry:'||p.id::text WHERE p.tenant_id=$1 AND p.id=$2`, tenant, id).Scan(&latched, &forced); err != nil || !latched || !forced {
			t.Fatalf("refused native/funding did not queue immediate compensation: id=%s latch=%t force=%t err=%v", id, latched, forced, err)
		}
	}
}

func assertMollieFirstPaymentRefusalQueuesRecovery(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, mode := range []string{"ineligible tier", "missing billing details", "linked native", "activated funding", "activated native", "established native", "other customer", "other tenant", "other tier", "unpaid funding", "mandate outage"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			tenant, current, target, funding, native := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,is_enterprise)
VALUES($1::uuid,$1::text,'Free',0,0,false),($2::uuid,$2::text,'Target',2,20,$3)`, current, target, mode != "missing billing details" && mode != "mandate outage")
			exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,payment_method,presentment_currency,billing_name,billing_email,billing_address)
VALUES($1,$2,'active','prepaid','mollie','EUR','Customer','billing@example.test','{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}')`, tenant, current)
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::text::uuid,'customer-'||$1::text)`, tenant)
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at)
VALUES($1::uuid,$2::text::uuid,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer-'||$2::text,'paid-first-'||$1::text,'mandate',NOW(),NOW()+INTERVAL '1 hour')`, funding, tenant, target)
			switch mode {
			case "missing billing details":
				exec(`UPDATE purser.tenant_subscriptions SET billing_email=NULL WHERE tenant_id=$1`, tenant)
			case "activated funding":
				exec(`UPDATE purser.payment_provider_intents SET activated_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			case "other customer":
				exec(`UPDATE purser.payment_provider_intents SET provider_customer_id='other' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			case "other tenant":
				exec(`UPDATE purser.payment_provider_intents SET tenant_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, funding, uuid.NewString())
			case "other tier":
				exec(`UPDATE purser.payment_provider_intents SET local_reference_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, funding, uuid.NewString())
			case "unpaid funding":
				exec(`UPDATE purser.payment_provider_intents SET status='provider_open',succeeded_at=NULL WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			}
			linked := mode == "linked native" || mode == "activated native" || mode == "established native"
			if linked {
				exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_subscription_id,funding_intent_id,expires_at)
VALUES($1::uuid,$2::text::uuid,'mollie','mollie_subscription_create','billing_tiers',$3,'provider_open','EUR',2000,$1::text,'customer-'||$2::text,'native-'||$1::text,$4,NOW()+INTERVAL '1 hour')`, native, tenant, target, funding)
				if mode == "established native" {
					exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='native-'||$2::text WHERE tenant_id=$1::text::uuid`, tenant, native)
				}
				if mode == "activated native" {
					exec(`UPDATE purser.payment_provider_intents SET status='succeeded',activated_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, native)
				}
			}
			client := &refusedFundingMollieClient{outage: mode == "mandate outage"}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			for range 2 {
				_, err := server.CreateMollieSubscription(ctx, &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: target, MandateId: "mandate"})
				wantCode := codes.FailedPrecondition
				if mode == "mandate outage" {
					wantCode = codes.Unavailable
				}
				if status.Code(err) != wantCode {
					t.Fatalf("refused activation code=%s want=%s: %v", status.Code(err), wantCode, err)
				}
			}
			var queued int
			err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.payment_provider_intents p JOIN purser.provider_webhook_inbox i ON i.provider='checkout_recovery' AND i.event_key='checkout-expiry:'||p.id::text
WHERE p.tenant_id=$1 AND p.recovery_started_at IS NOT NULL AND convert_from(i.raw_payload,'UTF8')::jsonb->>'force'='true' AND i.next_attempt_at<=NOW()`, tenant).Scan(&queued)
			want := 0
			if mode == "ineligible tier" || mode == "missing billing details" {
				want = 1
			}
			if mode == "linked native" {
				want = 2
			}
			if err != nil || queued != want || client.creates != 0 {
				t.Fatalf("refused funding queue=%d want=%d provider creates=%d err=%v", queued, want, client.creates, err)
			}
		})
	}
}

type refusedFundingMollieClient struct {
	fakeMollieBillingClient
	outage  bool
	creates int
}

func (c *refusedFundingMollieClient) GetMandate(_ context.Context, _ string, id string) (*mollielib.Mandate, error) {
	if c.outage {
		return nil, errors.New("temporary mandate read failure")
	}
	return &mollielib.Mandate{ID: id, Status: "valid"}, nil
}
func (c *refusedFundingMollieClient) CreateSubscription(context.Context, billingmollie.SubscriptionParams) (*mollielib.Subscription, error) {
	c.creates++
	return nil, errors.New("refused first payment reached subscription create")
}
