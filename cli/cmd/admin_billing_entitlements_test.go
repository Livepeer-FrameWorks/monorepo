package cmd

import (
	"context"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"reflect"
	"testing"
)

func TestEntitlementPatchPreservesUnchangedKeysAndExplicitZero(t *testing.T) {
	before := map[string]string{"max_concurrent_streams": "3", "custom_domain_enabled": "false", "recording_retention_days": "30"}
	got, err := entitlementPatch(before, []string{"recording_retention_days=0"}, []string{"max_concurrent_streams"})
	want := map[string]string{"custom_domain_enabled": "false", "recording_retention_days": "0"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("patch=%v err=%v", got, err)
	}
	if before["recording_retention_days"] != "30" || len(before) != 3 {
		t.Fatal("reviewed map was mutated")
	}
	for _, values := range [][]string{{"broken"}, {"a={"}, {"a=1", "a=2"}} {
		if _, err := entitlementPatch(before, values, nil); err == nil {
			t.Fatal("invalid patch accepted")
		}
	}
	if _, err := entitlementPatch(before, []string{"a=1"}, []string{"a"}); err == nil {
		t.Fatal("conflicting set/unset accepted")
	}
}

type legacySubscriptionTermsFixture struct{ legacyWrites int }

func (f *legacySubscriptionTermsFixture) GetSubscription(context.Context, string) (*purserpb.GetSubscriptionResponse, error) {
	return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{EntitlementOverrides: map[string]string{"storage_limit_gb": "10"}}}, nil
}
func (f *legacySubscriptionTermsFixture) UpdateSubscription(context.Context, *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	f.legacyWrites++
	return &purserpb.TenantSubscription{}, nil
}
func (f *legacySubscriptionTermsFixture) UpdateSubscriptionWithEntitlementReview(context.Context, *purserpb.UpdateSubscriptionRequest) (*purserpb.TenantSubscription, error) {
	return nil, status.Error(codes.Unimplemented, "older Purser")
}

func TestEntitlementReviewNeverFallsBackToAnUncheckedOlderServer(t *testing.T) {
	fixture := &legacySubscriptionTermsFixture{}
	err := runBillingEntitlements(t.Context(), io.Discard, fixture, "", "11111111-1111-4111-8111-111111111111", "reviewed change", []string{"storage_limit_gb=20"}, nil, false)
	if status.Code(err) != codes.Unimplemented || fixture.legacyWrites != 0 {
		t.Fatalf("reviewed update silently lost its precondition: error=%v unchecked writes=%d", err, fixture.legacyWrites)
	}
}
