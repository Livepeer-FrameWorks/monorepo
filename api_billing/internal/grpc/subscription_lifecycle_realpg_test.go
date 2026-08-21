//go:build schema_verify

package grpc

import (
	"context"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAccountOnboardingConvergence_RealPG(t *testing.T) {
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	prepaidTierID := uuid.NewString()
	postpaidTierID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
		    id, tier_name, display_name, tier_level, is_default_prepaid, is_default_postpaid, is_active
		) VALUES
		    ($1, $3, 'Default prepaid', 0, true, false, true),
		    ($2, $4, 'Default postpaid', 1, false, true, true)
	`, prepaidTierID, postpaidTierID, "onboarding-prepaid-"+uuid.NewString(), "onboarding-postpaid-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}
	tenantID := uuid.NewString()

	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, callErr := server.InitializePrepaidAccount(ctx, &purserpb.InitializePrepaidAccountRequest{TenantId: tenantID})
		errs <- callErr
	}()
	go func() {
		defer wg.Done()
		_, callErr := server.EnsureFreeAccount(ctx, &purserpb.InitializePostpaidAccountRequest{TenantId: tenantID})
		errs <- callErr
	}()
	wg.Wait()
	close(errs)
	for callErr := range errs {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}

	var subscriptions, balances int
	var canonicalTierID, billingModel string
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(tier_id::text), MAX(billing_model),
		       (SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1)
		FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&subscriptions, &canonicalTierID, &billingModel, &balances); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || balances != 1 {
		t.Fatalf("converged rows subscriptions/balances = %d/%d", subscriptions, balances)
	}
	if (canonicalTierID == prepaidTierID && billingModel != "prepaid") ||
		(canonicalTierID == postpaidTierID && billingModel != "postpaid") {
		t.Fatalf("canonical tier/model mismatch = %s/%s", canonicalTierID, billingModel)
	}
}

func TestSubscriptionLifecycleRepository_RealPG(t *testing.T) { //nolint:funlen // One engine lifecycle proves create, atomic replacement, events, and cancellation writeoff.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	tierID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, currency, is_active)
		VALUES ($1, $2, 'Lifecycle tier', 'EUR', true)
	`, tierID, "lifecycle-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	created, err := server.CreateSubscription(ctx, &purserpb.CreateSubscriptionRequest{
		TenantId: tenantID, TierId: tierID, BillingEmail: "first@example.com",
		BillingModel: "postpaid", PaymentMethod: "card",
		CustomFeatures: &purserpb.BillingFeatures{Recording: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, parseErr := uuid.Parse(created.GetId()); parseErr != nil {
		t.Fatalf("created subscription ID = %q: %v", created.GetId(), parseErr)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
		VALUES ($1, 'preserved_until_commit', 'true'::jsonb)
	`, created.GetId()); err != nil {
		t.Fatal(err)
	}

	invalidEmail := "must-rollback@example.com"
	_, err = server.UpdateSubscription(ctx, &purserpb.UpdateSubscriptionRequest{
		TenantId: tenantID, BillingEmail: &invalidEmail,
		ClearEntitlementOverrides: true,
		EntitlementOverrides:      map[string]string{"broken": "{"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid override error = %v", err)
	}
	var email string
	var preserved int
	if err := db.QueryRowContext(ctx, `
		SELECT billing_email,
		       (SELECT COUNT(*) FROM purser.subscription_entitlement_overrides WHERE subscription_id = ts.id)
		FROM purser.tenant_subscriptions ts WHERE tenant_id = $1
	`, tenantID).Scan(&email, &preserved); err != nil {
		t.Fatal(err)
	}
	if email != "first@example.com" || preserved != 1 {
		t.Fatalf("rollback state email/overrides = %q/%d", email, preserved)
	}

	updatedEmail := "updated@example.com"
	updatedMethod := "bank_transfer"
	updated, err := server.UpdateSubscription(ctx, &purserpb.UpdateSubscriptionRequest{
		TenantId: tenantID, BillingEmail: &updatedEmail, PaymentMethod: &updatedMethod,
		PricingOverrides: []*purserpb.PricingRule{{
			Meter: "delivered_minutes", Model: "tiered_graduated", Currency: "eur",
			IncludedQuantity: "250", UnitPrice: "0.0007", ConfigJson: "{}",
		}},
		EntitlementOverrides: map[string]string{"recording_retention_days": "60"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetBillingEmail() != updatedEmail || updated.GetPaymentMethod() != updatedMethod ||
		len(updated.GetPricingOverrides()) != 1 || updated.GetPricingOverrides()[0].GetCurrency() != "EUR" ||
		updated.GetEntitlementOverrides()["recording_retention_days"] != "60" {
		t.Fatalf("updated subscription = %+v", updated)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_collection_balances (tenant_id, currency, balance_cents)
		VALUES ($1, 'EUR', 73)
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.CancelSubscription(ctx, &purserpb.CancelSubscriptionRequest{TenantId: tenantID}); err != nil {
		t.Fatal(err)
	}
	var subscriptionStatus string
	var balance, writeoff int64
	var eventCount int
	if err := db.QueryRowContext(ctx, `
		SELECT ts.status, b.balance_cents,
		       (SELECT amount_cents FROM purser.billing_collection_writeoffs WHERE tenant_id = $1),
		       (SELECT COUNT(*) FROM purser.billing_event_outbox WHERE tenant_id = $1)
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_collection_balances b ON b.tenant_id = ts.tenant_id
		WHERE ts.tenant_id = $1
	`, tenantID).Scan(&subscriptionStatus, &balance, &writeoff, &eventCount); err != nil {
		t.Fatal(err)
	}
	if subscriptionStatus != "cancelled" || balance != 0 || writeoff != 73 || eventCount != 3 {
		t.Fatalf("cancel state = %q balance=%d writeoff=%d events=%d", subscriptionStatus, balance, writeoff, eventCount)
	}
}
