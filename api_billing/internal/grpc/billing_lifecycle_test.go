package grpc

import (
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

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
		pendingState string
	}{
		{"grace before the date", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(72 * time.Hour), FreeFallbackReason: "payment_failed"}, "scheduled", ""},
		{"date passed", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(-time.Minute), FreeFallbackReason: "payment_failed"}, "applying", ""},
		{"moved", purserdb.GetTenantBillingLifecycleRow{FreeFallbackAt: at(-time.Hour), FreeFallbackReason: "cancelled", FreeFallbackMoved: true}, "moved", ""},
		{"undoable downgrade", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(5 * 24 * time.Hour)}, "", "scheduled"},
		{"provider stop started", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(48 * time.Hour), ProviderStopStarted: true, FreeFallbackAt: at(-time.Minute), FreeFallbackReason: "cancelled"}, "applying", "final"},
		{"effective time passed", purserdb.GetTenantBillingLifecycleRow{PendingReason: "downgrade", PendingEffectiveAt: at(-time.Hour)}, "", "applying"},
		{"upgrade is not a downgrade state", purserdb.GetTenantBillingLifecycleRow{PendingReason: "tier_upgrade", PendingEffectiveAt: at(-time.Hour)}, "", ""},
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
			if tc.stage != "" && (resp.GetFreeFallback().GetReason() != tc.row.FreeFallbackReason || !resp.GetFreeFallback().GetAt().AsTime().Equal(tc.row.FreeFallbackAt.Time)) {
				t.Fatalf("free fallback = %+v, want reason %s at %s", resp.GetFreeFallback(), tc.row.FreeFallbackReason, tc.row.FreeFallbackAt.Time)
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
