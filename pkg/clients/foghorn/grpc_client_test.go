package foghorn

import (
	"testing"

	"frameworks/pkg/grpcutil"
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

func TestFoghornClientTLSConfigHonorsExplicitInsecureForFQDN(t *testing.T) {
	t.Parallel()

	cfg := foghornClientTLSConfig(GRPCConfig{
		GRPCAddr:      "foghorn.frameworks.example:18019",
		AllowInsecure: true,
	})
	if !cfg.AllowInsecure {
		t.Fatal("AllowInsecure = false, want true for explicit insecure")
	}
}

func TestFoghornClientTLSConfigKeepsLegacyDefaults(t *testing.T) {
	t.Parallel()

	fqdn := foghornClientTLSConfig(GRPCConfig{GRPCAddr: "foghorn.frameworks.example:18019"})
	if fqdn.AllowInsecure {
		t.Fatal("FQDN default AllowInsecure = true, want false")
	}
	local := foghornClientTLSConfig(GRPCConfig{GRPCAddr: "foghorn:18019"})
	if !local.AllowInsecure {
		t.Fatal("single-label default AllowInsecure = false, want true")
	}
}
