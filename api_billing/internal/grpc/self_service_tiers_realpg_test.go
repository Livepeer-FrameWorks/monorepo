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
