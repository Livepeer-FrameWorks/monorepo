package grpc

import (
	"context"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// A tenant fanout enqueues every object at once, and the workers claim them a
// few at a time. Every object compile asks Purser the same two questions about
// the tenant; the answers read once after the fanout serve every object it
// enqueued, whichever claim takes it. A row enqueued after that read reads
// again, so no compile uses Purser state from before its trigger.
func TestFanout_PurserCallsPerTenantConstant(t *testing.T) {
	fake := &purserEntitlementFake{
		subscription: func(context.Context, *purserpb.GetSubscriptionRequest) (*purserpb.GetSubscriptionResponse, error) {
			return &purserpb.GetSubscriptionResponse{Subscription: &purserpb.TenantSubscription{TierId: "tier-1"}}, nil
		},
		tier: func(context.Context, *purserpb.GetBillingTierRequest) (*purserpb.BillingTier, error) {
			return &purserpb.BillingTier{}, nil
		},
	}
	server, _, done := newMockServer(t)
	defer done()
	server.purserClient = startPurserEntitlementFake(t, fake)

	fanoutAt := time.Now().UTC()
	compile := func(triggeredAt, claimedAt time.Time) {
		t.Helper()
		ctx := withMediaAuthorityCompileMemo(context.Background(), server.compileMemoForClaim(), triggeredAt, claimedAt)
		if _, err := server.resolveProcessesJSONStrict(ctx, "tenant-1", "", "", "live"); err != nil {
			t.Fatal(err)
		}
	}
	const objects = 20
	firstClaim := fanoutAt.Add(mediaAuthorityCompileMemoClockMargin + time.Millisecond)
	for i := range objects {
		compile(fanoutAt, firstClaim.Add(time.Duration(i)*time.Millisecond))
	}
	if subscriptions, tiers := fake.subscriptionCalls.Load(), fake.tierCalls.Load(); subscriptions != 1 || tiers != 1 {
		t.Fatalf("a fanout of %d objects asked Purser for the subscription %d and the tier %d time(s), want once each", objects, subscriptions, tiers)
	}

	// A change after that read enqueues rows the read cannot serve.
	compile(firstClaim, firstClaim.Add(time.Second))
	if subscriptions := fake.subscriptionCalls.Load(); subscriptions != 2 {
		t.Fatalf("a row triggered after the shared read was served from it (%d subscription reads, want 2)", subscriptions)
	}
	// Outside an obligation worker every compile reads for itself.
	if _, err := server.resolveProcessesJSONStrict(context.Background(), "tenant-1", "", "", "live"); err != nil {
		t.Fatal(err)
	}
	if subscriptions := fake.subscriptionCalls.Load(); subscriptions != 3 {
		t.Fatalf("a compile outside a claim was served from the shared read (%d subscription reads, want 3)", subscriptions)
	}
}
