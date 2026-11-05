//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
)

func seedGrantRace(t *testing.T, db *sql.DB) (string, string, string) {
	t.Helper()
	tenant, oldTier, nextTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled) VALUES($1::text::uuid,$1::text,'Old',1,40,false),($2::text::uuid,$2::text,'Next',2,100,false)`, oldTier, nextTier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end) VALUES($1,$2,'active','postpaid','EUR',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 month')`, tenant, oldTier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,reason,assigned_tier_id) SELECT id,0,true,'concurrent grant',tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	return tenant, oldTier, nextTier
}

func TestOperatorAssignmentGrantSnapshot_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, oldTier, nextTier := seedGrantRace(t, db)
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	// nil models the earlier read that saw no grant; the transaction sees one.
	_, err := server.adminAssignTierOnce(t.Context(), tenant, nextTier, "postpaid", "stale assignment", nil)
	if !errors.Is(err, errPhaseChangedWhileRated) {
		t.Fatalf("stale grant snapshot was accepted: %v", err)
	}
	current, err := purserdb.New(db).GetSubscriptionTierForGrant(t.Context(), tenant)
	if err != nil || current.TierID != oldTier {
		t.Fatalf("stale assignment changed tier: %+v,%v", current, err)
	}
}

func TestOperatorAssignmentGrantSnapshot_RealYugabyte(t *testing.T) {
	TestOperatorAssignmentGrantSnapshot_RealPG(t)
}

func TestOperatorGrantEndCancellationRace_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	tenant, _, _ := seedGrantRace(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Errorf("rollback subscription race: %v", rollbackErr)
		}
	}()
	if _, err = purserdb.New(tx).LockTenantSubscriptionForOperatorAssignment(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	ended := make(chan error, 1)
	go func() { ended <- server.endOperatorGrant(ctx, tenant, "cancel raced revoke", false) }()
	for {
		var waiting bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockTenantSubscriptionForOperatorAssignment%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err = <-ended:
			t.Fatalf("grant end did not wait for its subscription lock: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET status='cancelled' WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-ended; err != nil {
		t.Fatalf("concurrent cancellation prevented grant cleanup: %v", err)
	}
	var grants, documents int
	if err = db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.subscription_operator_grants WHERE subscription_id=s.id),(SELECT count(*) FROM purser.billing_invoices WHERE tenant_id=s.tenant_id) FROM purser.tenant_subscriptions s WHERE tenant_id=$1`, tenant).Scan(&grants, &documents); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || documents != 0 {
		t.Fatalf("cancelled cleanup grants=%d documents=%d", grants, documents)
	}
}

type unavailableTierCache struct{ recordingCommodoreCache }

func (*unavailableTierCache) InvalidateTenantCache(context.Context, string, string) (*foghorncontrolpb.InvalidateTenantCacheResponse, error) {
	return nil, errors.New("Commodore unavailable")
}

func TestOperatorTierChangeCacheFailure_RealPG(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startOperatorGrantDatabase(t)
	for _, operation := range []string{"self", "operator", "promotion"} {
		t.Run(operation, func(t *testing.T) {
			tenant, oldTier, nextTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
			model := "postpaid"
			level := 1
			prepaid := operation == "promotion"
			if prepaid {
				model = "prepaid"
				level = 0
			}
			if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled,is_default_prepaid) VALUES($1::text::uuid,$1::text,'Old',$3,40,false,$4),($2::text::uuid,$2::text,'Next',2,100,false,false)`, oldTier, nextTier, level, prepaid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_name,billing_email,billing_address,payment_method,stripe_subscription_id,billing_period_start,billing_period_end) VALUES($1,$2,'active',$3,'EUR','Customer','billing@example.test','{"street":"Street 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}','stripe','sub_confirmed',NOW(),NOW()+INTERVAL '1 month')`, tenant, oldTier, model); err != nil {
				t.Fatal(err)
			}
			seedRPCTaxBuyer(t, db, tenant)
			if _, err := db.Exec(`INSERT INTO purser.prepaid_balances(tenant_id,currency,balance_cents) VALUES($1,'EUR',0)`, tenant); err != nil {
				t.Fatal(err)
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &unavailableTierCache{}}
			if operation == "self" {
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='sub',stripe_customer_id='customer' WHERE tenant_id=$1`, tenant); err != nil {
					t.Fatal(err)
				}
				// The provider fixture proves a retained EUR20 payment at old-price.
				if _, err := db.Exec(`UPDATE purser.billing_tiers SET base_price=20,stripe_price_id_monthly='old-price' WHERE id=$1`, oldTier); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE purser.billing_tiers SET base_price=50,stripe_price_id_monthly='new-price' WHERE id=$1`, nextTier); err != nil {
					t.Fatal(err)
				}
				start := time.Now().UTC().Truncate(time.Second)
				fixture := &upgradeProviderFixture{tenant: tenant, oldTier: oldTier, start: start, end: start.AddDate(0, 1, 0), stripeMetadata: map[string]string{"tenant_id": tenant, "tier_id": oldTier}}
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fixture.serve(t, w, r) }))
				t.Cleanup(provider.Close)
				previous := stripeapi.GetBackend(stripeapi.APIBackend)
				stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(provider.URL), HTTPClient: provider.Client(), MaxNetworkRetries: stripeapi.Int64(0)}))
				t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
				client := billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: server.logger})
				server.billing = handlers.NewService(db, server.logger, nil, nil, nil, client, nil)
				handlers.NewJobManager(db, server.logger, nil, nil, nil, nil, server.billing, nil)
			}
			var err error
			switch operation {
			case "self":
				_, err = server.ChangeBillingTier(t.Context(), &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: nextTier})
			case "operator":
				_, err = server.AdminAssignTier(operatorAssignCtx(uuid.NewString()), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: nextTier, Reason: "explicit tier assignment"})
			case "promotion":
				_, err = server.PromoteToPaid(t.Context(), &purserpb.PromoteToPaidRequest{TenantId: tenant, TierId: nextTier})
			}
			if err != nil {
				t.Fatalf("committed %s change reported failure: %v", operation, err)
			}
			current, err := purserdb.New(db).GetSubscriptionTierForGrant(t.Context(), tenant)
			if err != nil || current.TierID != nextTier || current.BillingModel != "postpaid" {
				t.Fatalf("change not committed: %+v,%v", current, err)
			}
			if operation == "self" {
				var refreshes int
				if err = db.QueryRow(`SELECT COUNT(*) FROM purser.media_authority_refresh_outbox WHERE tenant_id=$1 AND source_event_id LIKE 'tier-upgrade:%'`, tenant).Scan(&refreshes); err != nil || refreshes != 1 {
					t.Fatalf("committed upgrade lost its durable cache/media refresh: count=%d err=%v", refreshes, err)
				}
			}
		})
	}
}

func TestOperatorTierChangeCacheFailure_RealYugabyte(t *testing.T) {
	TestOperatorTierChangeCacheFailure_RealPG(t)
}
