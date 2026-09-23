package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Runs each credential through the real interceptor so the check sees the
// context exactly as a handler would.
func TestRequirePlatformOperatorJWT(t *testing.T) {
	secret := []byte("operator-test-secret")
	operatorJWT, err := auth.GenerateSessionJWT("user-op", "tenant-a", "op@example.test", "member", []string{auth.RolePlatformOperator}, time.Time{}, secret)
	if err != nil {
		t.Fatal(err)
	}
	ownerJWT, err := auth.GenerateJWT("user-owner", "tenant-a", "owner@example.test", "owner", secret)
	if err != nil {
		t.Fatal(err)
	}
	apiTokenJWT, err := auth.GenerateDelegatedAPITokenJWT("user-op", "tenant-a", "op@example.test", "member", "token-1", []string{"developer:read"}, "commodore", secret)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := GRPCAuthInterceptor(GRPCAuthConfig{
		ServiceToken:         "service-token",
		JWTSecret:            secret,
		MetadataPolicy:       MetadataPolicyAllow,
		DelegatedJWTAudience: "commodore",
	})
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Operator/Diagnose"}

	for _, tc := range []struct {
		name  string
		token string
		md    map[string]string
		want  codes.Code
	}{
		{name: "operator JWT", token: operatorJWT, want: codes.OK},
		{name: "tenant owner JWT", token: ownerJWT, want: codes.PermissionDenied},
		{name: "API token assertion", token: apiTokenJWT, want: codes.PermissionDenied},
		{name: "service token", token: "service-token", want: codes.PermissionDenied},
		{name: "service token naming a user", token: "service-token", md: map[string]string{"x-user-id": "user-op", "x-tenant-id": "tenant-a"}, want: codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pairs := map[string]string{"authorization": "Bearer " + tc.token}
			for k, v := range tc.md {
				pairs[k] = v
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.New(pairs))
			_, err := interceptor(ctx, struct{}{}, info, func(ctx context.Context, _ any) (any, error) {
				return nil, RequirePlatformOperatorJWT(ctx)
			})
			if got := status.Code(err); got != tc.want {
				t.Fatalf("code = %v, want %v (err %v)", got, tc.want, err)
			}
		})
	}
}

func TestRequirePlatformOperatorJWTRejectsBareContext(t *testing.T) {
	if status.Code(RequirePlatformOperatorJWT(context.Background())) != codes.PermissionDenied {
		t.Fatal("unauthenticated context admitted")
	}
}
