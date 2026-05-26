package resolvers

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestPublicTopologyReadContextHidesEndUserAuth(t *testing.T) {
	base := context.Background()
	base = context.WithValue(base, ctxkeys.KeyTenantID, "tenant-1")
	base = context.WithValue(base, ctxkeys.KeyJWTToken, "jwt")
	base = context.WithValue(base, ctxkeys.KeyAuthType, "jwt")
	base = context.WithValue(base, ctxkeys.KeyDemoMode, true)

	ctx := publicTopologyReadContext(base)

	if got := ctxkeys.GetTenantID(ctx); got != "" {
		t.Fatalf("tenant id = %q, want empty", got)
	}
	if got := ctxkeys.GetJWTToken(ctx); got != "" {
		t.Fatalf("jwt token = %q, want empty", got)
	}
	if got := ctxkeys.GetAuthType(ctx); got != "" {
		t.Fatalf("auth type = %q, want empty", got)
	}
	if !ctxkeys.IsDemoMode(ctx) {
		t.Fatal("demo mode should pass through")
	}
}
