//go:build schema_verify

package grpc

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func testGrantBoundaryDataCondition(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startOperatorGrantDatabase(t)
	tenant, paid, free := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled,is_default_prepaid) VALUES($1,'boundary-paid','Paid',2,50,false,false),($2,'boundary-free','Free',0,0,false,true)`, paid, free); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,presentment_currency,billing_period_start,billing_period_end) VALUES($1,$2,'active','postpaid','EUR',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 month')`, tenant, paid); err != nil {
		t.Fatal(err)
	}
	seedRPCTaxBuyer(t, db, tenant)
	server := &PurserServer{db: db, logger: logging.NewLogger(), tierReconciler: &assignTierReconciler{}, commodoreClient: &recordingCommodoreCache{}}
	op := operatorAssignCtx(uuid.NewString())
	if _, err := server.AdminSetBillingGrant(op, &purserpb.AdminSetBillingGrantRequest{TenantId: tenant, WaiveUsage: true, Collection: "invoice", Reason: "trial"}); err != nil {
		t.Fatal(err)
	}
	// Existing, dated subscriptions can contain conflicting native ownership.
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id='stripe',mollie_subscription_id='mollie',payment_method=NULL WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	_, err := server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, TierName: "boundary-free", Reason: "end trial"})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "both Stripe and Mollie") {
		t.Fatalf("unrepairable boundary response: %v; want FailedPrecondition with collection reason", err)
	}
	var grants int
	if err = db.QueryRow(`SELECT COUNT(*) FROM purser.subscription_operator_grants g JOIN purser.tenant_subscriptions s ON s.id=g.subscription_id WHERE s.tenant_id=$1`, tenant).Scan(&grants); err != nil || grants != 1 {
		t.Fatalf("failed revoke lost grant: %d %v", grants, err)
	}
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,mollie_subscription_id=NULL WHERE tenant_id=$1`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err = server.AdminRevokeBillingGrant(op, &purserpb.AdminRevokeBillingGrantRequest{TenantId: tenant, TierName: "boundary-free", Reason: "end trial after repair"}); err != nil {
		t.Fatal(err)
	}
	var model, tier string
	if err = db.QueryRow(`SELECT billing_model,tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&model, &tier); err != nil || model != "prepaid" || tier != free {
		t.Fatalf("grant revoke did not restore free: %s %s %v", model, tier, err)
	}
}
