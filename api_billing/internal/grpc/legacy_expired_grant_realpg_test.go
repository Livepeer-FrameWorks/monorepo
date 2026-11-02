//go:build schema_verify

package grpc

import (
	"testing"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

// An expired grant row without a recorded assignment has nothing to restore. It
// denies nothing, before or after the sweep can close it, while a grant that
// assigned its tier stays denied until it is closed.
func TestLegacyExpiredGrantKeepsAdmission_RealPG(t *testing.T) {
	db := startOperatorGrantDatabase(t)
	ctx := t.Context()
	tenant, tier := uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,stripe_price_id_monthly)
		VALUES ($1::text::uuid,$1::text,'Paid',2,50,'EUR',false,'paid-price')`, tier)
	exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,payment_method,stripe_customer_id,stripe_subscription_id,stripe_subscription_status,
		presentment_currency,billing_period_start,billing_period_end,next_billing_date)
		VALUES ($1,$2,'active','postpaid','stripe','customer','subscription','active','EUR',NOW()-INTERVAL '25 days',NOW()+INTERVAL '5 days',NOW()+INTERVAL '5 days')`, tenant, tier)
	exec(`INSERT INTO purser.subscription_operator_grants (subscription_id,waive_usage,collection,expires_at,reason,granted_by,granted_at)
		SELECT id,true,'provider',NOW()-INTERVAL '20 days','v0.3.13 trial','operator',NOW()-INTERVAL '60 days'
		FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant)
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	// Admission has a sub-second budget; a cold Yugabyte catalog can exceed it once.
	if _, err := purserdb.New(db).GetTenantAdmissionStatus(ctx, purserdb.GetTenantAdmissionStatusParams{TenantID: tenant, Currency: "EUR"}); err != nil {
		t.Fatal(err)
	}
	allowed := func(when string) {
		t.Helper()
		admission, err := server.GetTenantAdmissionStatus(serviceTestContext(), &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenant})
		if err != nil {
			t.Fatal(err)
		}
		if admission.GetIsSuspended() || !admission.GetCollectionReady() || admission.GetValidUntil() != nil {
			t.Fatalf("%s: suspended=%t collection_ready=%t valid_until=%v, want the tenant admitted with no grant deadline", when, admission.GetIsSuspended(), admission.GetCollectionReady(), admission.GetValidUntil())
		}
		status, err := server.GetTenantBillingStatus(serviceTestContext(), &purserpb.GetTenantBillingStatusRequest{TenantId: tenant})
		if err != nil {
			t.Fatal(err)
		}
		if status.GetGrantExpired() {
			t.Fatalf("%s: billing status reports an expired grant", when)
		}
	}
	allowed("first boot before the sweep")

	// Conflicting native ownership refuses the grant's billing boundary.
	exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='conflict',payment_method=NULL WHERE tenant_id=$1`, tenant)
	server.expireOperatorGrantsOnce(ctx)
	var failures int
	if err := db.QueryRowContext(ctx, `SELECT g.expiry_failures FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, tenant).Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("sweep with a refused boundary: failures=%d err=%v", failures, err)
	}
	exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id=NULL,payment_method='stripe' WHERE tenant_id=$1`, tenant)
	allowed("sweep refused by the billing boundary")

	exec(`UPDATE purser.subscription_operator_grants g SET expiry_attempted_at=NULL FROM purser.tenant_subscriptions s WHERE s.id=g.subscription_id AND s.tenant_id=$1`, tenant)
	server.expireOperatorGrantsOnce(ctx)
	var grants int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, tenant).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("sweep left the legacy grant: rows=%d err=%v", grants, err)
	}
	allowed("after the sweep")

	exec(`INSERT INTO purser.subscription_operator_grants (subscription_id,waive_usage,collection,expires_at,reason,assigned_tier_id)
		SELECT id,true,'provider',NOW()-INTERVAL '1 minute','current trial',tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant)
	admission, err := server.GetTenantAdmissionStatus(serviceTestContext(), &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenant})
	if err != nil || !admission.GetIsSuspended() || admission.GetValidUntil() == nil {
		t.Fatalf("an unclosed grant with an assignment must deny: %+v %v", admission, err)
	}
}
