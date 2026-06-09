package mesh

import (
	"fmt"
	"net"
	"testing"
)

func TestAllocateMeshIP_ExactDeterministicValues(t *testing.T) {
	cases := []struct {
		name, cluster, host, cidr, want string
	}{
		{"slash24 lands on .158", "cluster-a", "node-1", "10.42.0.0/24", "10.42.0.158"},
		{"slash28 lands on .14", "cluster-a", "node-1", "10.0.0.0/28", "10.0.0.14"},
		{"slash28 lands on reserved-boundary .2", "cluster-a", "node-2", "10.0.0.0/28", "10.0.0.2"},
		{"slash28 wraps modulo through reserved hosts to .2", "cluster-a", "wrap-6", "10.0.0.0/28", "10.0.0.2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip, err := AllocateMeshIP(c.cluster, c.host, mustCIDR(t, c.cidr), nil)
			if err != nil {
				t.Fatalf("AllocateMeshIP: %v", err)
			}
			if ip.String() != c.want {
				t.Fatalf("got %s, want %s", ip, c.want)
			}
		})
	}
}

func TestAllocateMeshIP_Slash28IsSmallestAccepted(t *testing.T) {
	if _, err := AllocateMeshIP("c", "h", mustCIDR(t, "10.0.0.0/28"), nil); err != nil {
		t.Fatalf("/28 (hostBits=4) must be accepted: %v", err)
	}
	if _, err := AllocateMeshIP("c", "h", mustCIDR(t, "10.0.0.0/29"), nil); err == nil {
		t.Fatal("/29 (hostBits=3) must be rejected")
	}
}

func TestAllocateMeshIP_ReturnsDotTwoWhenItIsTheOnlyFreeHost(t *testing.T) {
	cidr := mustCIDR(t, "10.0.0.0/28")
	taken := map[string]struct{}{}
	for i := 3; i <= 14; i++ {
		taken[fmt.Sprintf("10.0.0.%d", i)] = struct{}{}
	}
	ip, err := AllocateMeshIP("c", "h", cidr, taken)
	if err != nil {
		t.Fatalf("AllocateMeshIP: %v", err)
	}
	if ip.String() != "10.0.0.2" {
		t.Fatalf("got %s, want 10.0.0.2 (the sole free non-reserved host)", ip)
	}
}

func TestAllocateMeshIP_AllCandidatesStayInRange(t *testing.T) {
	cidr := mustCIDR(t, "10.0.0.0/28")
	base := net.ParseIP("10.0.0.0").To4()
	for h := 0; h < 200; h++ {
		ip, err := AllocateMeshIP("cluster", fmt.Sprintf("host-%d", h), cidr, nil)
		if err != nil {
			continue
		}
		if !cidr.Contains(ip) {
			t.Fatalf("host-%d: %s escaped CIDR", h, ip)
		}
		last := ip.To4()[3]
		if last == base[3] || last < 2 || last == 15 {
			t.Fatalf("host-%d: %s is a reserved host", h, ip)
		}
	}
}
