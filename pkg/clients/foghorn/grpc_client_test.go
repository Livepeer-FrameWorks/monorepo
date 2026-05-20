package foghorn

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestAddrIsFQDN(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		expected bool
	}{
		{name: "docker service name", addr: "foghorn", expected: false},
		{name: "localhost with port", addr: "localhost:50051", expected: false},
		{name: "ipv4", addr: "127.0.0.1", expected: false},
		{name: "ipv4 with port", addr: "127.0.0.1:50051", expected: false},
		{name: "ipv6 with port", addr: "[2001:db8::1]:50051", expected: false},
		{name: "fqdn", addr: "foghorn.cluster.frameworks.network", expected: true},
		{name: "fqdn with port", addr: "foghorn.cluster.frameworks.network:50051", expected: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := grpcutil.AddrIsFQDN(tc.addr); got != tc.expected {
				t.Fatalf("AddrIsFQDN(%q) = %t, want %t", tc.addr, got, tc.expected)
			}
		})
	}
}

func TestFoghornClientTLSConfigHonorsExplicitTLSForIPAddress(t *testing.T) {
	t.Parallel()

	cfg := foghornClientTLSConfig(GRPCConfig{
		GRPCAddr: "10.88.0.10:18019",
		UseTLS:   true,
	})
	if cfg.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false for explicit TLS")
	}
}

func TestFoghornClientTLSConfigUsesCAForSingleLabelAddress(t *testing.T) {
	t.Parallel()

	cfg := foghornClientTLSConfig(GRPCConfig{
		GRPCAddr:   "regional-us-1:18019",
		CACertFile: "/etc/frameworks/pki/ca.crt",
	})
	if cfg.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false when CA is configured")
	}
}

func TestFoghornClientTLSConfigHonorsExplicitInsecureForFQDN(t *testing.T) {
	t.Parallel()

	cfg := foghornClientTLSConfig(GRPCConfig{
		GRPCAddr:      "foghorn.frameworks.example:18029",
		AllowInsecure: true,
	})
	if !cfg.AllowInsecure {
		t.Fatal("AllowInsecure = false, want true for explicit insecure")
	}
}

func TestFoghornClientTLSConfigDevelopmentDefaults(t *testing.T) {
	t.Parallel()

	fqdn := foghornClientTLSConfig(GRPCConfig{GRPCAddr: "foghorn.frameworks.example:18029"})
	if fqdn.AllowInsecure {
		t.Fatal("FQDN default AllowInsecure = true, want false")
	}
	local := foghornClientTLSConfig(GRPCConfig{GRPCAddr: "foghorn:18019"})
	if !local.AllowInsecure {
		t.Fatal("single-label default AllowInsecure = false, want true")
	}
}

func TestFoghornClientTLSConfigProductionDoesNotDefaultToInsecure(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")

	cfg := foghornClientTLSConfig(GRPCConfig{GRPCAddr: "foghorn:18019"})
	if cfg.AllowInsecure {
		t.Fatal("AllowInsecure = true, want false for production default")
	}
}

func TestOutgoingAuthTokenPrefersConfiguredServiceToken(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyServiceToken, "context-service")

	if got := outgoingAuthToken(ctx, "configured-service"); got != "configured-service" {
		t.Fatalf("outgoingAuthToken() = %q, want configured service token", got)
	}
}

func TestAuthInterceptorSendsServiceTokenWithUserMetadata(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	err := authInterceptor("service-token")(ctx, "/foghorn.Test/Method", nil, nil, nil,
		func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
			md, ok := metadata.FromOutgoingContext(ctx)
			if !ok {
				t.Fatal("missing outgoing metadata")
			}
			if got := first(md.Get("authorization")); got != "Bearer service-token" {
				t.Fatalf("authorization = %q, want service token", got)
			}
			if got := first(md.Get("x-user-id")); got != "user-1" {
				t.Fatalf("x-user-id = %q, want user metadata", got)
			}
			if got := first(md.Get("x-tenant-id")); got != "tenant-1" {
				t.Fatalf("x-tenant-id = %q, want tenant metadata", got)
			}
			return nil
		})
	if err != nil {
		t.Fatalf("authInterceptor() error = %v", err)
	}
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
