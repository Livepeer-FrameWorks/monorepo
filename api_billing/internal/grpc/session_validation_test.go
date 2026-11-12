package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestPurserRejectsInteractiveTokenWithoutSessionState(t *testing.T) {
	secret := []byte("session-ingress-test")
	token, err := auth.GenerateJWT("user", "tenant", "user@example.test", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
	called := false
	_, err = middleware.GRPCAuthInterceptor(purserAuthConfig(GRPCServerConfig{JWTSecret: secret}))(
		ctx, nil, &grpcpkg.UnaryServerInfo{FullMethod: "/purser.BillingService/GetBillingStatus"},
		func(context.Context, any) (any, error) { called = true; return nil, nil })
	if status.Code(err) != codes.Unavailable || called {
		t.Fatalf("interactive JWT reached billing without authoritative session state: called=%v err=%v", called, err)
	}
}
