//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingmollie "frameworks/api_billing/internal/mollie"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	mollieapi "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
)

var errTaxWriteProbe = errors.New("provider unavailable after accepted tax preflight")

type taxWriteExpectationProbe struct {
	db      *sql.DB
	tenant  string
	calls   int
	problem error
}

func (p *taxWriteExpectationProbe) check(ctx context.Context, tenant, sha string) error {
	p.calls++
	var expected string
	err := p.db.QueryRowContext(ctx, `SELECT profile_sha256 FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid ORDER BY assessed_at DESC,id DESC LIMIT 1`, p.tenant).Scan(&expected)
	if err != nil || tenant != p.tenant || sha == "" || expected != sha {
		p.problem = fmt.Errorf("provider tax write preceded accepted expectation: tenant%s expected%s attempted%s err%v", tenant, expected, sha, err)
		return p.problem
	}
	return errTaxWriteProbe
}

type taxExpectationStripeClient struct {
	fakeStripeBillingClient
	probe *taxWriteExpectationProbe
}

func (c *taxExpectationStripeClient) PrepareCustomerTax(ctx context.Context, tenant, customer string, profile billingstripe.TaxProfile) (string, error) {
	return "", c.probe.check(ctx, tenant, profile.ProfileSHA)
}

type taxExpectationMollieClient struct {
	retainedFundingMollieClient
	probe *taxWriteExpectationProbe
}

func (c *taxExpectationMollieClient) CreateFirstPayment(ctx context.Context, params billingmollie.FirstPaymentParams) (*mollieapi.Payment, error) {
	if params.TaxTerms == nil {
		return nil, errors.New("taxed checkout omitted accepted terms")
	}
	return nil, c.probe.check(ctx, params.TenantID, params.TaxTerms.ProfileSHA)
}

func (c *taxExpectationMollieClient) CreateSubscription(ctx context.Context, params billingmollie.SubscriptionParams) (*mollieapi.Subscription, error) {
	if params.TaxTerms == nil {
		return nil, errors.New("native subscription omitted accepted terms")
	}
	return nil, c.probe.check(ctx, params.TenantID, params.TaxTerms.ProfileSHA)
}

func TestCheckoutWritesRetainIntendedTaxBeforeProviderIO_RealPG(t *testing.T) {
	for _, path := range []string{"stripe customer tax", "stripe cluster tax", "mollie first payment", "mollie subscription"} {
		t.Run(path, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, tier, free := seedFallbackLifecycle(t, db)
			prior, err := handlers.AssessProviderTaxProfileAt(t.Context(), db, tenant, "", billingtax.Checkout, time.Now().UTC().Add(-time.Hour))
			if err != nil || prior.RateBPS != 0 {
				t.Fatalf("retain original accepted zero-rate terms: %+v %v", prior, err)
			}
			raw, err := json.Marshal(prior)
			if err != nil {
				t.Fatal(err)
			}
			if err = purserdb.New(db).RetainProviderTaxExpectation(t.Context(), purserdb.RetainProviderTaxExpectationParams{TenantID: tenant, ProfileSha256: prior.ProfileSHA, AssessedAt: time.Now().UTC().Add(-time.Hour), Assessment: raw}); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET tier_id=$2,stripe_customer_id=NULL,stripe_subscription_id=NULL,mollie_subscription_id=NULL,status='active',free_fallback_at=NULL,free_fallback_provider_ended_at=NULL,billing_address='{"street":"Main1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1::uuid`, tenant, free); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`UPDATE purser.billing_tiers SET stripe_price_id_monthly='price_tax_probe' WHERE id=$1::uuid`, tier); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::uuid,'customer')`, tenant); err != nil {
				t.Fatal(err)
			}
			probe := &taxWriteExpectationProbe{db: db, tenant: tenant}
			mollie := &taxExpectationMollieClient{probe: probe}
			stripe := &taxExpectationStripeClient{probe: probe, fakeStripeBillingClient: fakeStripeBillingClient{customer: &stripeapi.Customer{ID: "customer"}}}
			server := &PurserServer{db: db, logger: logging.NewLogger(), stripeClient: stripe, mollieClient: mollie, commodoreClient: &fakeCommodorePrimaryUser{}}
			switch path {
			case "stripe customer tax":
				_, err = server.CreateCheckoutSession(t.Context(), &purserpb.CreateStripeCheckoutRequest{TenantId: tenant, TierId: tier, BillingPeriod: "monthly", SuccessUrl: "https://example.test/success", CancelUrl: "https://example.test/cancel"})
			case "stripe cluster tax":
				owner, cluster := uuid.NewString(), "tax-cluster-"+uuid.NewString()
				if _, err = db.Exec(`INSERT INTO purser.cluster_owners(tenant_id,status,payout_eligible) VALUES($1::uuid,'approved',true)`, owner); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`INSERT INTO purser.cluster_pricing(cluster_id,pricing_model,base_price,currency,stripe_price_id_monthly) VALUES($1,'monthly',20,'EUR','price_monthly')`, cluster); err != nil {
					t.Fatal(err)
				}
				server.quartermasterClient = &commercialQuartermasterStub{cluster: &quartermasterpb.InfrastructureCluster{ClusterId: cluster, OwnerTenantId: &owner}}
				success, cancel := "https://example.test/success", "https://example.test/cancel"
				_, err = server.CreateClusterSubscription(t.Context(), &purserpb.CreateClusterSubscriptionRequest{TenantId: tenant, ClusterId: cluster, SuccessUrl: &success, CancelUrl: &cancel})
			case "mollie first payment":
				_, err = server.CreateFirstPayment(t.Context(), &purserpb.CreateMollieFirstPaymentRequest{TenantId: tenant, TierId: tier, Method: "creditcard", RedirectUrl: "https://example.test/return"})
			case "mollie subscription":
				funding := uuid.NewString()
				if _, err = db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at) VALUES($1::uuid,$2::uuid,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','accepted-first-payment','mandate',NOW(),NOW()+INTERVAL '1 hour')`, funding, tenant, tier); err != nil {
					t.Fatal(err)
				}
				_, err = server.CreateMollieSubscription(t.Context(), &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: tier, MandateId: "mandate"})
			}
			if probe.calls != 1 || probe.problem != nil || err == nil {
				t.Fatalf("checkout lost expected tax before failing provider write: calls%d proof%v err%v", probe.calls, probe.problem, err)
			}
			var latestRate, count int
			if err = db.QueryRow(`SELECT (assessment->>'rate_bps')::int,(SELECT count(*) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid) FROM purser.provider_tax_assessments WHERE tenant_id=$1::uuid ORDER BY assessed_at DESC,id DESC LIMIT 1`, tenant).Scan(&latestRate, &count); err != nil || latestRate != 2100 || count != 2 {
				t.Fatalf("provider failure discarded accepted tax history: rate%d records%d err%v", latestRate, count, err)
			}
		})
	}
}
