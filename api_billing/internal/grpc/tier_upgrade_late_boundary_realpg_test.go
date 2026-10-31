//go:build schema_verify

package grpc

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"

	billingmollie "frameworks/api_billing/internal/mollie"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
)

func assertLateTierUpgradeRetryRefusesPurchase(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, provider := range []string{"stripe", "mollie", "advance", "invoice"} {
		t.Run("late boundary/"+provider, func(t *testing.T) {
			ctx := t.Context()
			tenant, old, target, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			end := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			start := end.Add(-30 * 24 * time.Hour)
			requested := end.Add(-time.Hour)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled,stripe_price_id_monthly)
VALUES($1::uuid,$1::text,'Old',2,20,false,'old-price'),($2::uuid,$2::text,'Target',3,50,false,'new-price')`, old, target)
			subscription := ""
			if provider == "stripe" {
				subscription = "sub"
			}
			exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end,pending_tier_id,pending_reason)
VALUES($1,$2,'active','postpaid','stripe','customer',NULLIF($3,''),'Customer','billing@example.com','{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}','EUR',$4,$5,NULL,NULL)`, tenant, old, subscription, start, end)
			exec(`INSERT INTO purser.billing_invoices(tenant_id,status,amount,base_amount,due_date,base_fee_period_start,base_fee_period_end,presentment_amount_cents,presentment_currency,presentment_units_per_eur,presentment_reference_date,finalized_at)
VALUES($1,'paid',20,20,NOW(),$2,$3,2000,'EUR',1,CURRENT_DATE,NOW())`, tenant, start, end)
			fixture := &upgradeProviderFixture{tenant: tenant, oldTier: old, start: start, end: end, metadata: map[string]any{"tenant_id": tenant}, stripeMetadata: map[string]string{"tenant_id": tenant, "tier_id": old}}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
			defer httpServer.Close()
			oldBackend := stripeapi.GetBackend(stripeapi.APIBackend)
			stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(httpServer.URL), HTTPClient: httpServer.Client(), MaxNetworkRetries: stripeapi.Int64(0)}))
			defer stripeapi.SetBackend(stripeapi.APIBackend, oldBackend)
			var mollieClient *billingmollie.Client
			if provider == "mollie" {
				endpoint, err := url.Parse(httpServer.URL)
				if err != nil {
					t.Fatal(err)
				}
				mollieClient, err = billingmollie.NewClient(billingmollie.Config{APIKey: "test_fixture", Logger: logging.NewLogger(), HTTPClient: &http.Client{Transport: upgradeMollieTransport{base: http.DefaultTransport, endpoint: endpoint}}})
				if err != nil {
					t.Fatal(err)
				}
				subscription = "sub"
				exec(`UPDATE purser.tenant_subscriptions SET payment_method='mollie',stripe_subscription_id=NULL,mollie_subscription_id='sub' WHERE tenant_id=$1`, tenant)
				exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant)
			}
			logger := logging.NewLogger()
			client := billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_fixture", Logger: logger})
			service := handlers.NewService(db, logger, nil, nil, mollieClient, client, nil)
			handlers.NewJobManager(db, logger, nil, nil, nil, nil, service, nil)
			stageErr := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
				_, err := service.StageTierUpgradeTx(ctx, tx, tenant, target, start, end)
				return err
			})
			if !errors.Is(stageErr, billing.ErrTierUpgradeRejected) || !strings.Contains(stageErr.Error(), "roll over") {
				t.Fatalf("late initial purchase did not reach its period guard: %v", stageErr)
			}
			var staged int
			if readErr := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.subscription_tier_upgrades WHERE tenant_id=$1`, tenant).Scan(&staged); readErr != nil || staged != 0 {
				t.Fatalf("late initial purchase left staged work: %d %v", staged, readErr)
			}

			exec(`INSERT INTO purser.subscription_tier_upgrades(id,tenant_id,old_tier_id,tier_id,actor_id,old_period_start,old_period_end,requested_at,provider,collection_provider,customer_id,provider_subscription_id,stripe_price_id,old_base_cents,base_cents)
VALUES($1,$2,$3,$4,'operator',$5,$6,$7,$8,'stripe','customer',$9,'new-price',2000,5000)`, id, tenant, old, target, start, end, requested, provider, subscription)
			exec(`UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='tier_upgrade' WHERE tenant_id=$1`, tenant, target)

			done, err := service.ProcessTierUpgrade(ctx, tenant, id)
			if done || !errors.Is(err, billing.ErrTierUpgradeRejected) {
				t.Fatalf("late retry was not terminally refused: done=%t err=%v", done, err)
			}
			var state, actual string
			var invoices int
			if err = db.QueryRowContext(ctx, `SELECT u.state,s.tier_id::text,(SELECT count(*) FROM purser.billing_invoices i WHERE i.tenant_id=s.tenant_id AND i.base_fee_period_start<>$3) FROM purser.subscription_tier_upgrades u JOIN purser.tenant_subscriptions s ON s.tenant_id=u.tenant_id WHERE u.tenant_id=$1 AND u.id=$2`, tenant, id, start).Scan(&state, &actual, &invoices); err != nil {
				t.Fatal(err)
			}
			if state != "cancelled" || actual != old || invoices != 0 || fixture.updates != 0 || fixture.charges != 0 {
				t.Fatalf("late retry purchased a fresh term: state=%s tier=%s invoices=%d updates=%d charges=%d", state, actual, invoices, fixture.updates, fixture.charges)
			}
			done, err = service.ProcessTierUpgrade(ctx, tenant, id)
			if done || err != nil || fixture.updates != 0 || fixture.charges != 0 {
				t.Fatalf("terminal retry changed purchase: done=%t err=%v updates=%d charges=%d", done, err, fixture.updates, fixture.charges)
			}
		})
	}
}
