package resolvers

import (
	"context"
	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"testing"
)

func TestEnablePrepaidUsageRequiresBillingManagerAndForwardsOwnTenant(t *testing.T) {
	fake := &clientstest.FakePurser{EnablePrepaidUsageFn: func(_ context.Context, tenant string) (*purserpb.EnablePrepaidUsageResponse, error) {
		if tenant != "tenant-own" {
			t.Fatal("target tenant not derived from identity")
		}
		return &purserpb.EnablePrepaidUsageResponse{Activated: true, TierName: "payg", BillingModel: "prepaid"}, nil
	}}
	r := purserResolver(fake)
	for _, ctx := range []context.Context{context.Background(), context.WithValue(clientstest.AuthedCtx("tenant-own"), ctxkeys.KeyRole, "member")} {
		if _, err := r.DoEnablePrepaidUsage(ctx); err == nil {
			t.Fatal("non-manager enabled paid usage")
		}
	}
	if fake.Calls != 0 {
		t.Fatal("unauthorized request reached backend")
	}
	ctx := context.WithValue(clientstest.AuthedCtx("tenant-own"), ctxkeys.KeyRole, "owner")
	out, err := r.DoEnablePrepaidUsage(ctx)
	if err != nil || !out.Activated {
		t.Fatalf("opt in=%+v err=%v", out, err)
	}
}
