//go:build schema_verify

package grpc

import (
	"testing"
	"time"

	"frameworks/api_billing/internal/handlers"
	billingstripe "frameworks/api_billing/internal/stripe"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Staging, re-pointing and cancelling each commit one durable provider step for
// the tenant; a tier a recurring price cannot reach is refused, not promised.
func TestChangeBillingTierQueuesProviderStep_RealPG(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenant, paid, low, lower, free := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,stripe_price_id_monthly) VALUES
		($1::text::uuid,$1::text,'Paid',4,50,'EUR',false,'paid-price'),($2::text::uuid,$2::text,'Low',3,30,'EUR',false,'low-price'),
		($3::text::uuid,$3::text,'Lower',2,20,'EUR',false,'lower-price'),($4::text::uuid,$4::text,'No fee',1,0,'EUR',false,NULL)`, paid, low, lower, free)
	exec(`INSERT INTO purser.tenant_subscriptions (billing_customer_type,billing_company,tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,
		billing_name,billing_email,billing_address,presentment_currency,billing_period_start,billing_period_end,next_billing_date)
		VALUES ('business','Fixture Ltd',$1,$2,'active','postpaid','stripe','customer','subscription','Customer','billing@example.com',
		'{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}','EUR',NOW()-INTERVAL '10 days',NOW()+INTERVAL '20 days',NOW()+INTERVAL '20 days')`, tenant, paid)
	logger := logging.NewLogger()
	stripeClient := billingstripe.NewClient(billingstripe.Config{SecretKey: "sk_test_fixture", Logger: logger})
	server := &PurserServer{db: db, logger: logger, tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{},
		billing: handlers.NewService(db, logger, nil, nil, nil, stripeClient, nil)}
	change := func(tier string) (*purserpb.ChangeBillingTierResponse, error) {
		return server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenant, TierId: tier})
	}
	steps := func() (rows int, pendingTier string) {
		t.Helper()
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1::text AND status='pending'),
			(SELECT COALESCE(pending_tier_id::text,'') FROM purser.tenant_subscriptions WHERE tenant_id=$1::text::uuid)`, tenant).Scan(&rows, &pendingTier); err != nil {
			t.Fatal(err)
		}
		return
	}
	if resp, err := change(low); err != nil || resp.GetPendingTierId() != low {
		t.Fatalf("stage downgrade: %+v %v", resp, err)
	}
	if rows, pending := steps(); rows != 1 || pending != low {
		t.Fatalf("staged downgrade: provider steps=%d pending=%s, want one step for %s", rows, pending, low)
	}
	if _, err := change(lower); err != nil {
		t.Fatalf("re-point downgrade: %v", err)
	}
	if rows, pending := steps(); rows != 1 || pending != lower {
		t.Fatalf("re-pointed downgrade: provider steps=%d pending=%s, want one step for %s", rows, pending, lower)
	}
	if _, err := change(free); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a no-fee tier that is not Free = %v, want FailedPrecondition", err)
	}
	exec(`UPDATE purser.billing_tiers SET base_price=25 WHERE id=$1`, free)
	if _, err := change(free); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a paid tier without a Stripe price = %v, want FailedPrecondition", err)
	}
	if _, pending := steps(); pending != lower {
		t.Fatalf("refused target replaced the staged downgrade: %s", pending)
	}
	exec(`UPDATE purser.provider_webhook_inbox SET status='processed',processed_at=NOW() WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1`, tenant)
	// Cancelling needs no billing profile: keeping the tier starts no new commitment.
	exec(`UPDATE purser.tenant_subscriptions SET billing_name=NULL,billing_address=NULL WHERE tenant_id=$1`, tenant)
	resp, err := change(paid)
	if err != nil || resp.GetMessage() != "Scheduled downgrade cancelled" {
		t.Fatalf("select the current tier: %+v %v", resp, err)
	}
	if rows, pending := steps(); rows != 1 || pending != "" {
		t.Fatalf("cancelled downgrade: provider steps=%d pending=%s, want the step reopened and nothing staged", rows, pending)
	}
	exec(`UPDATE purser.tenant_subscriptions SET billing_name='Customer',
		billing_address='{"street":"Street 1","city":"Leiden","postal_code":"2332ED","country":"NL"}' WHERE tenant_id=$1`, tenant)

	exec(`UPDATE purser.provider_webhook_inbox SET status='processed',processed_at=NOW() WHERE provider='tier_downgrade' AND headers->>'tenant_id'=$1`, tenant)
	exec(`UPDATE purser.tenant_subscriptions SET pending_tier_id=$2,pending_reason='downgrade',pending_effective_at=$3 WHERE tenant_id=$1`, tenant, lower, time.Now().Add(time.Hour))
	if _, err = server.AdminAssignTier(operatorAssignCtx(uuid.NewString()), &purserpb.AdminAssignTierRequest{TenantId: tenant, TierName: low, Reason: "contract change"}); err != nil {
		t.Fatalf("operator assignment over a staged downgrade: %v", err)
	}
	if rows, pending := steps(); rows != 1 || pending != "" {
		t.Fatalf("assignment over a downgrade: provider steps=%d pending=%s, want the provider converged", rows, pending)
	}
}
