package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTierDowngradeRecoveryRequiresInteractivePlatformOperator(t *testing.T) {
	for _, kind := range []string{"service", "api_token", "jwt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, kind)
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
			if kind != "jwt" {
				ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
			}
			_, err := (&PurserServer{}).AdminRecoverTierDowngrade(ctx, &purserpb.AdminRecoverTierDowngradeRequest{})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("credential %s bypassed operator check: %v", kind, err)
			}
		})
	}
}

func TestTierDowngradeRecoveryValidatesConfirmation(t *testing.T) {
	ctx := context.WithValue(t.Context(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	const tenant = "00000000-0000-4000-8000-000000000001"
	for _, r := range []*purserpb.AdminRecoverTierDowngradeRequest{
		{TenantId: "bad"},
		{TenantId: tenant, Confirmed: true, TargetTierId: "tier"},
		{TenantId: tenant, Confirmed: true, Reason: "pricing fixed"},
	} {
		if _, err := (&PurserServer{}).AdminRecoverTierDowngrade(ctx, r); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request %v = %v, want InvalidArgument", r, err)
		}
	}
}
