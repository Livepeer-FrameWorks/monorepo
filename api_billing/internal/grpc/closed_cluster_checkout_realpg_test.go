//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
)

type closingClusterStripeClient struct {
	fakeStripeBillingClient
	onCreate      func(context.Context) error
	reservationID string
}

func (c *closingClusterStripeClient) CreateCheckoutSession(ctx context.Context, params billingstripe.CheckoutSessionParams) (*stripeapi.CheckoutSession, error) {
	c.reservationID = params.ReservationID
	session, err := c.fakeStripeBillingClient.CreateCheckoutSession(ctx, params)
	if err == nil && c.onCreate != nil {
		err = c.onCreate(ctx)
	}
	return session, err
}

func TestClosedAccountRefusesMonthlyClusterPurchase_RealPG(t *testing.T) {
	for _, tc := range []struct {
		name, currency string
		duringProvider bool
	}{{"Stripe before request", "EUR", false}, {"Purser before request", "USD", false}, {"Stripe during provider call", "EUR", true}} {
		t.Run(tc.name, func(t *testing.T) {
			currency := tc.currency
			db := startOperatorGrantDatabase(t)
			tenant, _, _ := seedGrantRace(t, db)
			owner := uuid.NewString()
			cluster := "monthly-" + uuid.NewString()
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.cluster_owners(tenant_id,status,payout_eligible) VALUES($1,'approved',true)`, owner); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.cluster_pricing(cluster_id,pricing_model,base_price,currency,stripe_price_id_monthly) VALUES($1,'monthly',20,'EUR','price-monthly')`, cluster); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `UPDATE purser.tenant_subscriptions SET presentment_currency=$2,payment_method='stripe' WHERE tenant_id=$1`, tenant, currency); err != nil {
				t.Fatal(err)
			}
			qm := &commercialQuartermasterStub{cluster: &quartermasterpb.InfrastructureCluster{ClusterId: cluster, OwnerTenantId: &owner}}
			provider := &closingClusterStripeClient{fakeStripeBillingClient: fakeStripeBillingClient{customer: &stripeapi.Customer{ID: "customer"}, session: &stripeapi.CheckoutSession{ID: "checkout", URL: "https://example.com/checkout"}}}
			server := &PurserServer{db: db, logger: logging.NewLogger(), quartermasterClient: qm, stripeClient: provider}
			successURL, cancelURL := "https://example.com/ok", "https://example.com/cancel"
			req := &purserpb.CreateClusterSubscriptionRequest{TenantId: tenant, ClusterId: cluster, SuccessUrl: &successURL, CancelUrl: &cancelURL}
			if tc.duringProvider {
				var intentID string
				provider.onCreate = func(ctx context.Context) error {
					q := purserdb.New(db)
					if err := q.RecordTenantBillingClosure(ctx, purserdb.RecordTenantBillingClosureParams{TenantID: tenant, ClosedAt: time.Now().UTC(), EventID: uuid.NewString()}); err != nil {
						return err
					}
					if err := db.QueryRowContext(ctx, `SELECT id::text FROM purser.payment_provider_intents WHERE tenant_id=$1 AND provider='stripe' AND idempotency_key=$2`, tenant, provider.checkoutKey).Scan(&intentID); err != nil {
						return err
					}
					if err := q.StartCheckoutCompensation(ctx, purserdb.StartCheckoutCompensationParams{TenantID: tenant, IntentID: intentID}); err != nil {
						return err
					}
					if err := q.RequestCheckoutRecovery(ctx, purserdb.RequestCheckoutRecoveryParams{TenantID: tenant, IntentID: intentID, Reason: "account closed during provider call"}); err != nil {
						return err
					}
					rows, err := q.FinishCheckoutCompensation(ctx, purserdb.FinishCheckoutCompensationParams{TenantID: tenant, IntentID: intentID, Outcome: "expired", ExpectedReferences: []byte(`{"session_id":null,"payment_id":null,"subscription_id":null}`)})
					if err != nil {
						return err
					}
					if rows != 1 {
						t.Fatal("fixture did not close the unbound request")
					}
					_, err = db.ExecContext(ctx, `UPDATE purser.provider_webhook_inbox SET status='processed' WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+intentID)
					return err
				}
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				_, err := server.CreateClusterSubscription(ctx, req)
				assertTenantRefusal(t, err, RefusalAccountClosed)
				if provider.checkoutKey == "" {
					t.Fatalf("in-flight provider response bypassed closure: %v", err)
				}
				row, err := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant, IntentID: intentID})
				if err != nil || row.Status != "expired" || row.ProviderSessionID.String != "checkout" || row.CompensationCurrent || provider.reservationID != intentID {
					t.Fatalf("late session reopened or escaped the recovered receipt: %+v %v", row, err)
				}
				var retry string
				if err := db.QueryRowContext(t.Context(), `SELECT status FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+intentID).Scan(&retry); err != nil || retry != "pending" {
					t.Fatalf("late response did not wake recovery: %s %v", retry, err)
				}
				return
			}
			response, err := server.CreateClusterSubscription(t.Context(), req)
			if err != nil || response == nil || currency == "EUR" && provider.checkoutKey == "" || currency == "USD" && qm.materialized == nil {
				t.Fatalf("positive control did not create the separate monthly purchase under a grant: response=%v err=%v", response, err)
			}
			if err := purserdb.New(db).RecordTenantBillingClosure(t.Context(), purserdb.RecordTenantBillingClosureParams{TenantID: tenant, ClosedAt: time.Now().UTC(), EventID: uuid.NewString()}); err != nil {
				t.Fatal(err)
			}
			provider.checkoutKey = ""
			qm.materialized = nil
			_, err = server.CreateClusterSubscription(t.Context(), req)
			assertTenantRefusal(t, err, RefusalAccountClosed)
			if provider.checkoutKey != "" || qm.materialized != nil {
				t.Fatal("closed purchase reached the provider or materialized cluster access")
			}
		})
	}
}

func TestClosedAccountRefusesMonthlyClusterPurchase_RealYugabyte(t *testing.T) {
	TestClosedAccountRefusesMonthlyClusterPurchase_RealPG(t)
}
