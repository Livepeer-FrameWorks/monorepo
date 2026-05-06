package grpc

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthorizeNodeLifecycleAllowsServiceForSharedNode(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	if err := authorizeNodeLifecycle(ctx, &state.NodeState{}); err != nil {
		t.Fatalf("authorizeNodeLifecycle service auth: %v", err)
	}
}

func TestAuthorizeNodeLifecycleRejectsJWTForSharedNode(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-a")

	err := authorizeNodeLifecycle(ctx, &state.NodeState{})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
	}
}

func TestAuthorizeNodeLifecycleAllowsOwningTenantJWT(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-a")

	if err := authorizeNodeLifecycle(ctx, &state.NodeState{TenantID: "tenant-a"}); err != nil {
		t.Fatalf("authorizeNodeLifecycle owner jwt: %v", err)
	}
}
