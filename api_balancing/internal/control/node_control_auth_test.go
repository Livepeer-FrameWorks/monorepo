package control

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestNodeControlAuthInterceptorAcceptsTenantJWT(t *testing.T) {
	secret := []byte("test-secret")
	token, err := auth.GenerateJWT("user-1", "tenant-1", "user@example.test", "owner", secret)
	if err != nil {
		t.Fatalf("GenerateJWT: %v", err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	interceptor := nodeControlAuthInterceptor("service-token", string(secret), nil)
	info := &grpc.UnaryServerInfo{FullMethod: foghornpb.NodeControlService_GetNodeHealth_FullMethodName}

	called := false
	_, err = interceptor(ctx, nil, info, func(context.Context, any) (any, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("node control JWT auth rejected: %v", err)
	}
	if !called {
		t.Fatal("handler was not called")
	}
}
