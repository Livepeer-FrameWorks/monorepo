package mesh

import (
	"encoding/base64"
	"net"
	"testing"
)

func TestGenerateKeyPairShape(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	privBytes, err := base64.StdEncoding.DecodeString(priv)
	if err != nil {
		t.Fatalf("private not base64: %v", err)
	}
	if len(privBytes) != 32 {
		t.Fatalf("private key length = %d, want 32", len(privBytes))
	}
	// WireGuard clamp bits.
	if privBytes[0]&7 != 0 {
		t.Errorf("private key low bits not clamped: %#x", privBytes[0])
	}
	if privBytes[31]&128 != 0 {
		t.Errorf("private key high bit not cleared: %#x", privBytes[31])
	}
	if privBytes[31]&64 == 0 {
		t.Errorf("private key bit 254 not set: %#x", privBytes[31])
	}
	pubBytes, err := base64.StdEncoding.DecodeString(pub)
	if err != nil {
		t.Fatalf("public not base64: %v", err)
	}
	if len(pubBytes) != 32 {
		t.Fatalf("public key length = %d, want 32", len(pubBytes))
	}
}

func TestAllocateMeshIPDeterministic(t *testing.T) {
	_, cidr, err := net.ParseCIDR("10.88.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	taken := map[string]struct{}{}
	ip1, err := AllocateMeshIP("production", "regional-eu-1", cidr, taken)
	if err != nil {
		t.Fatalf("alloc 1: %v", err)
	}
	ip2, err := AllocateMeshIP("production", "regional-eu-1", cidr, taken)
	if err != nil {
		t.Fatalf("alloc 2: %v", err)
	}
	if !ip1.Equal(ip2) {
		t.Fatalf("same inputs produced different IPs: %s vs %s", ip1, ip2)
	}
	if !cidr.Contains(ip1) {
		t.Fatalf("allocated IP %s outside CIDR %s", ip1, cidr)
	}
}

func TestAllocateMeshIPCollisionAvoidance(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.88.0.0/16")
	first, err := AllocateMeshIP("production", "regional-eu-1", cidr, nil)
	if err != nil {
		t.Fatal(err)
	}
	taken := map[string]struct{}{first.String(): {}}
	second, err := AllocateMeshIP("production", "regional-eu-1", cidr, taken)
	if err != nil {
		t.Fatal(err)
	}
	if first.Equal(second) {
		t.Fatalf("collision not avoided: %s", first)
	}
}

func TestAllocateMeshIPReservedSkipped(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.88.0.0/28") // 13 usable host addresses
	seen := map[string]struct{}{}
	for _, host := range []string{"a", "b", "c", "d"} {
		ip, err := AllocateMeshIP("production", host, cidr, seen)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		last := ip.To4()[3]
		if last < 2 || last == 15 {
			t.Fatalf("%s: got reserved address %s", host, ip)
		}
		seen[ip.String()] = struct{}{}
	}
}
