package grpc

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestBillingLifecycleStatesTheMoveToFreeAndDowngradeProgress(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) sql.NullTime { return sql.NullTime{Time: now.Add(d), Valid: true} }
	for _, tc := range []struct {
		name         string
		row          purserdb.GetTenantBillingLifecycleRow
		stage        string
		moveAt       sql.NullTime
		pendingState string
	}{
		{"grace before the date", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(72 * time.Hour), FreeFallbackReason: "payment_failed"}, "scheduled", at(72 * time.Hour), ""},
		{"date passed", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(-time.Minute), FreeFallbackReason: "payment_failed"}, "applying", at(-time.Minute), ""},
		{"moved", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(-time.Hour), FreeFallbackReason: "cancelled", FreeFallbackMoved: true}, "moved", at(-time.Hour), ""},
		{"undoable downgrade", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(5 * 24 * time.Hour)}, "", sql.NullTime{}, "scheduled"},
		// ReconcileProviderTier backdates the fallback inside the stop lead; the
		// paid tier still runs to the downgrade's effective time.
		{"staged move to Free inside its stop lead", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(48 * time.Hour), FreeFallbackAt: at(-time.Minute), FreeFallbackReason: "cancelled"}, "scheduled", at(48 * time.Hour), "scheduled"},
		{"provider stop started", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(48 * time.Hour), ProviderStopStarted: true, FreeFallbackAt: at(-time.Minute), FreeFallbackReason: "cancelled"}, "scheduled", at(48 * time.Hour), "final"},
		{"staged move to Free past its effective time", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(-time.Hour), ProviderStopStarted: true, FreeFallbackAt: at(-73 * time.Hour), FreeFallbackReason: "cancelled"}, "applying", at(-time.Hour), "final"},
		{"effective time passed", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(-time.Hour)}, "", sql.NullTime{}, "applying"},
		{"upgrade is not a downgrade state", purserdb.GetTenantBillingLifecycleRow{PendingReason: "tier_upgrade", PendingEffectiveAt: at(-time.Hour)}, "", sql.NullTime{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &purserpb.BillingStatusResponse{}
			tc.row.EffectiveBasePrice = "0.00"
			applyBillingLifecycle(resp, tc.row, now)
			if resp.GetEffectiveBasePrice() != "0.00" {
				t.Fatalf("effective base price = %q", resp.GetEffectiveBasePrice())
			}
			if got := resp.GetFreeFallback().GetStage(); got != tc.stage {
				t.Fatalf("free fallback stage = %q, want %q", got, tc.stage)
			}
			if tc.stage != "" && (resp.GetFreeFallback().GetReason() != tc.row.FreeFallbackReason || !resp.GetFreeFallback().GetAt().AsTime().Equal(tc.moveAt.Time)) {
				t.Fatalf("free fallback = %+v, want reason %s at %s", resp.GetFreeFallback(), tc.row.FreeFallbackReason, tc.moveAt.Time)
			}
			if got := resp.GetPendingTierState(); got != tc.pendingState {
				t.Fatalf("pending tier state = %q, want %q", got, tc.pendingState)
			}
		})
	}
}

func TestTenantSafeOperatorGrantDropsOperatorBookkeeping(t *testing.T) {
	base := "0.00"
	grant := tenantSafeOperatorGrant(&purserpb.OperatorBillingGrant{
		SubscriptionId: "sub", BasePrice: &base, WaiveUsage: true, Collection: "invoice",
		ExpiresAt: timestamppb.New(time.Unix(1_800_000_000, 0)), Reason: "QA account for the release team",
		GrantedBy: "operator@example.com", Active: true, FallbackTierName: "developer",
		ExpiryError: "provider timeout", ExpiryFailures: 3,
	})
	if grant.GetReason() != "" || grant.GetGrantedBy() != "" || grant.GetExpiryError() != "" || grant.GetExpiryFailures() != 0 {
		t.Fatalf("tenant grant leaks operator fields: %+v", grant)
	}
	if grant.GetBasePrice() != "0.00" || !grant.GetWaiveUsage() || grant.GetCollection() != "invoice" || grant.GetFallbackTierName() != "developer" || grant.GetExpiresAt() == nil {
		t.Fatalf("tenant grant lost its terms: %+v", grant)
	}
}

func TestBillingLifecycleStatesWhenADowngradeTakesEffectAndWhenAMoveToFreeIsFinal(t *testing.T) {
	periodEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	row := purserdb.GetTenantBillingLifecycleRow{
		DowngradeEffectiveAt:   sql.NullTime{Time: periodEnd, Valid: true},
		PendingTierDisplayName: "Developer", FallbackTierDisplayName: "Supporter",
	}
	for _, provider := range []string{"stripe", "mollie"} {
		resp := &purserpb.BillingStatusResponse{CollectionProvider: provider, OperatorGrant: &purserpb.OperatorBillingGrant{FallbackTierName: "supporter"}}
		applyBillingLifecycle(resp, row, periodEnd.Add(-10*24*time.Hour))
		if !resp.GetDowngradeEffectiveAt().AsTime().Equal(periodEnd) {
			t.Fatalf("%s: downgrade effective at = %v, want %v", provider, resp.GetDowngradeEffectiveAt(), periodEnd)
		}
		if want := periodEnd.Add(-handlers.FreeDowngradeStopLead); !resp.GetFreeMoveFinalAt().AsTime().Equal(want) {
			t.Fatalf("%s: free move final at = %v, want %v", provider, resp.GetFreeMoveFinalAt(), want)
		}
		if resp.GetPendingTierDisplayName() != "Developer" || resp.GetOperatorGrant().GetFallbackTierDisplayName() != "Supporter" {
			t.Fatalf("%s: display names = %q / %q", provider, resp.GetPendingTierDisplayName(), resp.GetOperatorGrant().GetFallbackTierDisplayName())
		}
	}
	// Without a provider subscription a move to Free has nothing to stop.
	for _, provider := range []string{"", "operator"} {
		resp := &purserpb.BillingStatusResponse{CollectionProvider: provider}
		applyBillingLifecycle(resp, row, periodEnd.Add(-10*24*time.Hour))
		if resp.GetFreeMoveFinalAt() != nil {
			t.Fatalf("collection %q: free move final at = %v, want unset", provider, resp.GetFreeMoveFinalAt())
		}
	}
}
