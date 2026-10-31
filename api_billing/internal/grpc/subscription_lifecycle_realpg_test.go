//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

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
	t.Run("new prepaid subscription periods", func(t *testing.T) { assertDefaultSubscriptionPeriods(t, db, prepaidTierID, "prepaid") })
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
		CustomFeatures: &purserpb.BillingFeatures{ProcessingCustomizable: true, SupportLevel: "dedicated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, parseErr := uuid.Parse(created.GetId()); parseErr != nil {
		t.Fatalf("created subscription ID = %q: %v", created.GetId(), parseErr)
	}
	var storedFeatures []byte
	if err := db.QueryRowContext(ctx, `SELECT custom_features FROM purser.tenant_subscriptions WHERE id = $1`, created.GetId()).Scan(&storedFeatures); err != nil {
		t.Fatal(err)
	}
	if got := scanBillingFeatures(storedFeatures); !got.GetProcessingCustomizable() || got.GetSupportLevel() != "dedicated" {
		t.Fatalf("stored custom_features %s read back as %+v", storedFeatures, got)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
		VALUES ($1, 'preserved_until_commit', 'true'::jsonb)
	`, created.GetId()); err != nil {
		t.Fatal(err)
	}

	invalidEmail := "must-rollback@example.com"
	_, err = server.UpdateSubscription(serviceTestContext(), &purserpb.UpdateSubscriptionRequest{
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
	updated, err := server.UpdateSubscription(serviceTestContext(), &purserpb.UpdateSubscriptionRequest{
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

func assertDefaultSubscriptionPeriods(t *testing.T, db *sql.DB, tierID, model string) {
	t.Helper()
	tier, err := uuid.Parse(tierID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, at, end string }{
		{"January nonleap", "2025-01-31T12:30:00Z", "2025-02-28T12:30:00Z"},
		{"January leap", "2024-01-31T12:30:00Z", "2024-02-29T12:30:00Z"},
		{"October month end", "2026-10-31T12:30:00Z", "2026-11-30T12:30:00Z"},
		{"UTC rollover", "2025-01-31T23:30:00-05:00", "2025-03-01T04:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, parseErr := time.Parse(time.RFC3339, tc.at)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			wantEnd, parseErr := time.Parse(time.RFC3339, tc.end)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			tenant := uuid.NewString()
			q := purserdb.New(db)
			params := purserdb.EnsureDefaultTenantSubscriptionParams{ID: uuid.New(), TenantID: tenant, TierID: tier, BillingModel: model, Now: sql.NullTime{Time: at, Valid: true}}
			rows, writeErr := q.EnsureDefaultTenantSubscription(t.Context(), params)
			if writeErr != nil || rows != 1 {
				t.Fatalf("create initial subscription: rows=%d err=%v", rows, writeErr)
			}
			var start, end, next sql.NullTime
			read := func() {
				t.Helper()
				if readErr := db.QueryRowContext(t.Context(), `SELECT billing_period_start,billing_period_end,next_billing_date FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&start, &end, &next); readErr != nil {
					t.Fatal(readErr)
				}
			}
			read()
			if !start.Valid || !start.Time.Equal(at) || !end.Valid || !end.Time.Equal(wantEnd) || !next.Valid || !next.Time.Equal(wantEnd) {
				t.Fatalf("initial billing period is missing or not the capped UTC month: start=%v end=%v next=%v want=%s/%s", start, end, next, at.UTC(), wantEnd)
			}
			params.ID = uuid.New()
			params.Now = sql.NullTime{Time: at.AddDate(2, 0, 0), Valid: true}
			rows, writeErr = q.EnsureDefaultTenantSubscription(t.Context(), params)
			if writeErr != nil || rows != 0 {
				t.Fatalf("existing subscription not a no-op: rows=%d err=%v", rows, writeErr)
			}
			read()
			if !start.Time.Equal(at) || !end.Time.Equal(wantEnd) || !next.Time.Equal(wantEnd) {
				t.Fatalf("replay rewrote an existing billing period: start=%v end=%v next=%v", start, end, next)
			}
		})
	}
}
