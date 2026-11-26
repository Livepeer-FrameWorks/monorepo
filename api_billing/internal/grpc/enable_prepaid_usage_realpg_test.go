//go:build schema_verify

package grpc

import (
	"context"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
	"time"
)

func TestExplicitFreeRetainsCreditUntilPAYGOptIn_RealPG(t *testing.T) {
	configureRPCTaxSupplier(t)
	db := startPurserTransitionRealPG(t)
	free, payg, paid := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,is_default_prepaid,is_default_postpaid,metering_enabled) VALUES ($1,'free','Free',1,0,'EUR',false,true,false),($2,'payg','PAYG',0,0,'EUR',true,false,false),($3,'supporter','Supporter',2,79,'EUR',false,false,false)`, free, payg, paid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO purser.tier_entitlements (tier_id,key,value) VALUES ($1,'storage_limit_gb','10'),($1,'recording_retention_days','30')`, free); err != nil {
		t.Fatal(err)
	}
	service := handlers.NewService(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	service.SetFreeTierEligibilityReader(func(context.Context, string, handlers.FreeTierLimits) (bool, error) { return true, nil })
	server := &PurserServer{db: db, logger: logging.NewLogger(), billing: service, tierReconciler: &assignTierReconciler{}}
	seed := func(tier, model string, balance int64) string {
		tenant := uuid.NewString()
		now := time.Now().UTC()
		if _, err := db.Exec(`INSERT INTO purser.tenant_subscriptions (tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,next_billing_date,billing_email,billing_name,billing_customer_type,billing_address,presentment_currency,automatic_prepaid_usage) VALUES ($1,$2,'active',$3,$4,$5,$5,'buyer@example.test','Buyer LLC','business','{"street":"Main1","city":"Seattle","postal_code":"98101","country":"US"}','EUR',true)`, tenant, tier, model, now.Add(-time.Hour), now.AddDate(0, 1, 0)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO purser.prepaid_balances (tenant_id,currency,balance_cents) VALUES ($1,'EUR',$2)`, tenant, balance); err != nil {
			t.Fatal(err)
		}
		return tenant
	}
	initialFree := seed(free, "postpaid", 912)
	for range 2 {
		if _, err := server.PromoteToPaid(billingActorContext("jwt", initialFree, "owner"), &purserpb.PromoteToPaidRequest{TenantId: initialFree, TierId: free}); err != nil {
			t.Fatal(err)
		}
	}
	initial, err := server.GetSubscription(t.Context(), &purserpb.GetSubscriptionRequest{TenantId: initialFree})
	if err != nil || initial.GetSubscription().GetAutomaticPrepaidUsage() {
		t.Fatalf("already-Free choice was treated as a no-op: %v", err)
	}
	if changed, err := service.ActivateFundedFree(t.Context(), initialFree); err != nil || changed {
		t.Fatalf("already-Free choice was undone: %v %v", changed, err)
	}
	tenant := seed(payg, "prepaid", 912)
	if _, err := server.PromoteToPaid(billingActorContext("jwt", tenant, "owner"), &purserpb.PromoteToPaidRequest{TenantId: tenant, TierId: free}); err != nil {
		t.Fatal(err)
	}
	current, err := server.GetSubscription(t.Context(), &purserpb.GetSubscriptionRequest{TenantId: tenant})
	if err != nil || current.GetSubscription().GetAutomaticPrepaidUsage() {
		t.Fatalf("explicit Free choice was not retained: %v", err)
	}
	if changed, err := service.ActivateFundedFree(t.Context(), tenant); err != nil || changed {
		t.Fatalf("worker undid explicit Free choice: %v %v", changed, err)
	}
	out, err := server.EnablePrepaidUsage(billingActorContext("jwt", tenant, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: tenant})
	if err != nil || !out.GetActivated() || out.GetAwaitingFunds() {
		t.Fatalf("opt in=%+v err=%v", out, err)
	}
	if _, err = server.EnablePrepaidUsage(billingActorContext("jwt", tenant, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: tenant}); err != nil {
		t.Fatalf("opt-in replay: %v", err)
	}
	var retained int64
	if err = db.QueryRow(`SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id=$1 AND currency='EUR'`, tenant).Scan(&retained); err != nil || retained != 912 {
		t.Fatalf("retained balance=%d err=%v", retained, err)
	}
	empty := seed(free, "postpaid", 0)
	if _, err = db.Exec(`UPDATE purser.tenant_subscriptions SET automatic_prepaid_usage=false WHERE tenant_id=$1`, empty); err != nil {
		t.Fatal(err)
	}
	out, err = server.EnablePrepaidUsage(billingActorContext("jwt", empty, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: empty})
	if err != nil || out.GetActivated() || !out.GetAwaitingFunds() {
		t.Fatalf("empty funding opt-in=%+v err=%v", out, err)
	}
	customTier := uuid.NewString()
	if _, err = db.Exec(`INSERT INTO purser.billing_tiers (id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES ($1,'custom_prepaid','Custom prepaid',0,0,'EUR',false)`, customTier); err != nil {
		t.Fatal(err)
	}
	custom := seed(customTier, "prepaid", 912)
	out, err = server.EnablePrepaidUsage(billingActorContext("jwt", custom, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: custom})
	if err != nil || !out.GetActivated() || out.GetTierName() != "custom_prepaid" {
		t.Fatalf("existing prepaid arrangement was rejected or replaced: %+v %v", out, err)
	}
	monthly := seed(paid, "postpaid", 912)
	if _, err = server.EnablePrepaidUsage(billingActorContext("jwt", monthly, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: monthly}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("paid plan replaced: %v", err)
	}
	if _, err = server.EnablePrepaidUsage(billingActorContext("jwt", tenant, "owner"), &purserpb.EnablePrepaidUsageRequest{TenantId: monthly}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-tenant opt-in admitted: %v", err)
	}
}
