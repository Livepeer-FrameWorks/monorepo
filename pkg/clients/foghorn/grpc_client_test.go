package foghorn

import "testing"

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
			if got := addrIsFQDN(tc.addr); got != tc.expected {
				t.Fatalf("addrIsFQDN(%q) = %t, want %t", tc.addr, got, tc.expected)
			}
		})
	}
}
