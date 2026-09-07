package resolvers

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func billingAPITokenContext(permission string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	return context.WithValue(ctx, ctxkeys.KeyPermissions, []string{permission})
}

func TestDoChangeBillingTierRequiresBillingWriteScope(t *testing.T) {
	backendCalls := 0
	r := purserResolver(&clientstest.FakePurser{ChangeBillingTierFn: func(context.Context, string, string) (*purserpb.ChangeBillingTierResponse, error) {
		backendCalls++
		return &purserpb.ChangeBillingTierResponse{Success: true}, nil
	}})
	if _, err := r.DoChangeBillingTier(billingAPITokenContext("billing:read"), "tier-pro"); err == nil {
		t.Fatal("read-only API token changed billing tier")
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls after denied scope = %d", backendCalls)
	}
	if _, err := r.DoChangeBillingTier(billingAPITokenContext("billing:write"), "tier-pro"); err != nil {
		t.Fatal(err)
	}
	if backendCalls != 1 {
		t.Fatalf("authorized backend calls = %d", backendCalls)
	}
}

func TestDoChangeBillingTierRejectsCoarseWriteScope(t *testing.T) {
	backendCalls := 0
	r := purserResolver(&clientstest.FakePurser{ChangeBillingTierFn: func(context.Context, string, string) (*purserpb.ChangeBillingTierResponse, error) {
		backendCalls++
		return &purserpb.ChangeBillingTierResponse{Success: true}, nil
	}})
	if _, err := r.DoChangeBillingTier(billingAPITokenContext("write"), "tier-pro"); err == nil {
		t.Fatal("coarse write scope changed billing tier")
	}
	if backendCalls != 0 {
		t.Fatalf("coarse write backend calls = %d", backendCalls)
	}
}

func TestDoChangeBillingTierRejectsSameTenantMember(t *testing.T) {
	backendCalls := 0
	r := purserResolver(&clientstest.FakePurser{ChangeBillingTierFn: func(context.Context, string, string) (*purserpb.ChangeBillingTierResponse, error) {
		backendCalls++
		return &purserpb.ChangeBillingTierResponse{Success: true}, nil
	}})
	ctx := context.WithValue(clientstest.AuthedCtx("tenant-1"), ctxkeys.KeyRole, "member")
	if _, err := r.DoChangeBillingTier(ctx, "tier-pro"); err == nil {
		t.Fatal("same-tenant member changed billing tier")
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls = %d", backendCalls)
	}
}
