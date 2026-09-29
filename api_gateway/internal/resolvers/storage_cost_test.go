package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

type storagePricingPurser struct {
	purser.Interface
	calls int
}

func (p *storagePricingPurser) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	p.calls++
	return &purserpb.GetTenantBillingStatusResponse{StoragePricing: &purserpb.StoragePricing{UnitPricePerGibMonth: 0.03, Currency: "EUR"}}, nil
}

// Tier pricing is served to callers with billing:read only: an API token
// without that scope gets no projection and causes no Purser call (which
// Purser would refuse), while a session sees the projection.
func TestStorageCostProjectionFollowsBillingReadScope(t *testing.T) {
	fake := &storagePricingPurser{}
	r := &Resolver{Clients: &clients.ServiceClients{Purser: fake}, Logger: logging.NewLogger()}
	withTenant := func(authType string, permissions ...string) context.Context {
		ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
		ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, authType)
		ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, permissions)
		return WithStoragePricingCache(ctx)
	}

	projection, err := r.ProjectStorageCostForCaller(withTenant("api_token", "streams:read"), 1<<30)
	if err != nil || projection != nil || fake.calls != 0 {
		t.Fatalf("token without billing:read: projection %+v err %v purser calls %d, want none", projection, err, fake.calls)
	}

	projection, err = r.ProjectStorageCostForCaller(withTenant("api_token", "billing:read"), 1<<30)
	if err != nil || projection == nil || projection.PerMonth != 0.03 || projection.Currency != "EUR" {
		t.Fatalf("token with billing:read: projection %+v err %v", projection, err)
	}

	projection, err = r.ProjectStorageCostForCaller(withTenant("jwt"), 1<<30)
	if err != nil || projection == nil || projection.PerMonth != 0.03 {
		t.Fatalf("session: projection %+v err %v", projection, err)
	}
}
