//go:build schema_verify

package grpc

import (
	"testing"

	"frameworks/api_billing/internal/database/purserdb"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

func TestBillingTaxReviewBannerScopesCurrentProvider_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	tenant, otherTenant, tier := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level) VALUES($1,$2,'Review fixture',1)`, tier, "review-"+tier)
	exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status,billing_name,billing_email,billing_customer_type,billing_address,stripe_customer_id,stripe_subscription_id,mollie_subscription_id) VALUES($1,$2,'postpaid','active','Buyer','buyer@example.test','business','{"street":"Main 1","city":"Vienna","postal_code":"1010","country":"AT"}','cus_current','sub_current','mol_current')`, tenant, tier)
	server := &PurserServer{db: db}
	assertReview := func(want bool) {
		t.Helper()
		details, err := server.GetBillingDetails(t.Context(), &purserpb.GetBillingDetailsRequest{TenantId: tenant})
		if err != nil || !details.GetIsComplete() || details.GetTaxReviewRequired() != want {
			t.Fatalf("current-provider review=%v details=%+v err=%v", want, details, err)
		}
	}
	insertReview := func(owner, network, kind, ref string) {
		t.Helper()
		exec(`INSERT INTO purser.crypto_accounting_anomalies(tenant_id,kind,network,reference_type,reference_id,detail,evidence_json) VALUES($1,'provider_tax_review',$2,$3,$4,'Check saved VAT details','{"review_required":true,"fallback_rate_bps":2100}')`, owner, network, kind, ref)
	}
	insertReview(otherTenant, "stripe", "stripe_subscription", "sub_current")
	assertReview(false)
	exec(`DELETE FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND reference_type='stripe_subscription' AND reference_id='sub_current'`, otherTenant)
	exec(`INSERT INTO purser.crypto_accounting_anomalies(tenant_id,kind,network,reference_type,reference_id,detail,evidence_json) VALUES($1,'provider_tax_review','stripe','stripe_subscription','sub_current','Destination policy requires operator review','{"reason":"foreign_tax_policy_required"}')`, tenant)
	assertReview(false)
	exec(`DELETE FROM purser.crypto_accounting_anomalies WHERE tenant_id=$1 AND reference_type='stripe_subscription' AND reference_id='sub_current'`, tenant)
	insertReview(tenant, "stripe", "stripe_subscription", "sub_current")
	assertReview(true)
	exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved' WHERE tenant_id=$1 AND reference_type='stripe_subscription' AND reference_id='sub_current'`, tenant)
	assertReview(true)
	resolved, err := purserdb.New(db).ResolveProviderTaxReview(t.Context(), purserdb.ResolveProviderTaxReviewParams{TenantID: tenant, ReferenceType: "stripe_subscription", ReferenceID: "sub_current", Note: "Current legal billing profile and exclusive tax terms synchronized"})
	if err != nil || resolved != 1 {
		t.Fatalf("valid provider sync did not clear the customer notice: rows=%d err=%v", resolved, err)
	}
	assertReview(false)
	resolved, err = purserdb.New(db).ResolveProviderTaxReview(t.Context(), purserdb.ResolveProviderTaxReviewParams{TenantID: tenant, ReferenceType: "stripe_subscription", ReferenceID: "sub_current", Note: "Current legal billing profile and exclusive tax terms synchronized"})
	if err != nil || resolved != 0 {
		t.Fatalf("unchanged valid sync churned an acknowledged review: rows=%d err=%v", resolved, err)
	}
	insertReview(tenant, "stripe", "stripe_subscription", "sub_old")
	insertReview(tenant, "stripe", "stripe_customer", "cus_current")
	assertReview(false)
	insertReview(tenant, "mollie", "mollie_subscription", "mol_current")
	assertReview(true)
	exec(`UPDATE purser.crypto_accounting_anomalies SET status='resolved' WHERE tenant_id=$1 AND reference_type='mollie_subscription' AND reference_id='mol_current'`, tenant)
	assertReview(true)
	if err := purserdb.New(db).ResolveMollieSubscriptionTaxReview(t.Context(), purserdb.ResolveMollieSubscriptionTaxReviewParams{TenantID: tenant, SubscriptionID: "mol_current"}); err != nil {
		t.Fatal(err)
	}
	assertReview(false)
	exec(`UPDATE purser.tenant_subscriptions SET status='ended' WHERE tenant_id=$1`, tenant)
	assertReview(false)
}
