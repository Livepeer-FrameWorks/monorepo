package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
)

func TestMediaPlacementPermissionBoundary(t *testing.T) {
	for _, test := range []struct {
		name, role, authType, permission, tenant string
		manage, allow                            bool
	}{
		{"owner session write", "owner", "jwt", "", "tenant", true, true},
		{"admin session write", "admin", "jwt", "", "tenant", true, true},
		{"member read", "member", "jwt", "", "tenant", false, true},
		{"member write", "member", "jwt", "", "tenant", true, false},
		{"viewer read", "viewer", "jwt", "", "tenant", false, true},
		{"stream write is not placement", "owner", "api_token", "streams:write", "tenant", true, false},
		{"billing write is not placement", "owner", "api_token", "billing:write", "tenant", true, false},
		{"placement read cannot write", "owner", "api_token", "placement:read", "tenant", true, false},
		{"scoped owner write", "owner", "api_token", "placement:write", "tenant", true, true},
		{"member token cannot elevate role", "member", "api_token", "placement:write", "tenant", true, false},
		{"scoped read", "member", "api_token", "placement:read", "tenant", false, true},
		{"cross tenant read", "owner", "api_token", "placement:read", "other", false, false},
		{"cross tenant write", "owner", "api_token", "placement:write", "other", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := ctxAs("user", "tenant", test.role)
			ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, test.authType)
			ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{test.permission})
			err := requireMediaPlacementAccess(ctx, test.tenant, test.manage)
			if (err == nil) != test.allow {
				t.Fatalf("access=%v want allow=%t", err, test.allow)
			}
		})
	}
	if err := requireMediaPlacementAccess(context.Background(), "tenant", false); err == nil {
		t.Fatal("anonymous read allowed")
	}
}
