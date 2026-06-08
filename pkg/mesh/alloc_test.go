package mesh

import (
	"encoding/base64"
	"fmt"
	"net"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// TestDerivePublicKey_MatchesX25519 pins the wgtypes-based derivation to the
// classic curve25519.X25519(priv, Basepoint) output for a properly clamped
// private key. This guards the migration off the manual derivation that
// previously lived in api_mesh/internal/agent/static.go.
func TestDerivePublicKey_MatchesX25519(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	// WireGuard private-key clamping (RFC 7748).
	raw[0] &= 248
	raw[31] &= 127
	raw[31] |= 64

	privB64 := base64.StdEncoding.EncodeToString(raw)

	gotPub, err := DerivePublicKey(privB64)
	if err != nil {
		t.Fatalf("DerivePublicKey: %v", err)
	}

	wantRaw, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("curve25519.X25519: %v", err)
	}
	wantPub := base64.StdEncoding.EncodeToString(wantRaw)

	if gotPub != wantPub {
		t.Fatalf("public key mismatch:\n  got:  %s\n  want: %s", gotPub, wantPub)
	}
}

func TestDerivePublicKey_RoundTrip(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	derived, err := DerivePublicKey(priv)
	if err != nil {
		t.Fatalf("DerivePublicKey: %v", err)
	}
	if derived != pub {
		t.Fatalf("round-trip mismatch:\n  generated: %s\n  derived:   %s", pub, derived)
	}
}

func TestDerivePublicKey_RejectsInvalid(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"empty", ""},
		{"not base64", "not_base64!"},
		{"wrong length", base64.StdEncoding.EncodeToString([]byte("too short"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := DerivePublicKey(c.in); err == nil {
				t.Fatalf("expected error for %q, got nil", c.in)
			}
		})
	}
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return n
}

// TestAllocateMeshIP_Deterministic pins the load-bearing property that makes
// allocation safe to run from both the CLI and Quartermaster: the same
// (cluster, host) pair always resolves to the same IP, so re-runs are
// idempotent rather than handing out a second address. The result must also
// stay inside the CIDR and avoid the reserved .0/.1/broadcast hosts.
func TestAllocateMeshIP_Deterministic(t *testing.T) {
	cidr := mustCIDR(t, "10.42.0.0/24")

	first, err := AllocateMeshIP("cluster-a", "node-1", cidr, nil)
	if err != nil {
		t.Fatalf("AllocateMeshIP: %v", err)
	}
	second, err := AllocateMeshIP("cluster-a", "node-1", cidr, nil)
	if err != nil {
		t.Fatalf("AllocateMeshIP (repeat): %v", err)
	}
	if !first.Equal(second) {
		t.Fatalf("non-deterministic: %s != %s", first, second)
	}
	if !cidr.Contains(first) {
		t.Fatalf("allocated IP %s outside CIDR %s", first, cidr)
	}
	if last := first.To4()[3]; last == 0 || last == 1 || last == 255 {
		t.Fatalf("allocated reserved host .%d (must skip .0/.1/broadcast)", last)
	}

	// A different host in the same cluster must not collide with the first.
	other, err := AllocateMeshIP("cluster-a", "node-2", cidr, nil)
	if err != nil {
		t.Fatalf("AllocateMeshIP (other host): %v", err)
	}
	if other.Equal(first) {
		t.Fatalf("distinct hosts mapped to the same IP %s", first)
	}
}

// TestAllocateMeshIP_SkipsTaken proves the probe-forward path: when the
// deterministic first choice is already in `taken`, allocation must return a
// different in-CIDR address rather than re-handing the taken one.
func TestAllocateMeshIP_SkipsTaken(t *testing.T) {
	cidr := mustCIDR(t, "10.42.0.0/24")

	want, err := AllocateMeshIP("cluster-a", "node-1", cidr, nil)
	if err != nil {
		t.Fatalf("AllocateMeshIP: %v", err)
	}
	taken := map[string]struct{}{want.String(): {}}
	got, err := AllocateMeshIP("cluster-a", "node-1", cidr, taken)
	if err != nil {
		t.Fatalf("AllocateMeshIP (taken): %v", err)
	}
	if got.Equal(want) {
		t.Fatalf("returned a taken IP %s", got)
	}
	if !cidr.Contains(got) {
		t.Fatalf("fallback IP %s outside CIDR %s", got, cidr)
	}
}

// TestAllocateMeshIP_Errors pins the failure modes: a non-IPv4 mask, a CIDR too
// small to carve a usable host range, and full exhaustion each return an error
// instead of a bogus or colliding address.
func TestAllocateMeshIP_Errors(t *testing.T) {
	t.Run("rejects non-IPv4 CIDR", func(t *testing.T) {
		if _, err := AllocateMeshIP("c", "h", mustCIDR(t, "fd00::/64"), nil); err == nil {
			t.Fatal("expected error for IPv6 CIDR")
		}
	})

	t.Run("rejects CIDR smaller than /28", func(t *testing.T) {
		if _, err := AllocateMeshIP("c", "h", mustCIDR(t, "10.0.0.0/29"), nil); err == nil {
			t.Fatal("expected error for /29 (fewer than 4 host bits)")
		}
	})

	t.Run("returns error when CIDR is exhausted", func(t *testing.T) {
		// /28 → 16 hosts; .0/.1/.15 are reserved, leaving .2–.14 usable.
		taken := map[string]struct{}{}
		for i := 2; i <= 14; i++ {
			taken[fmt.Sprintf("10.0.0.%d", i)] = struct{}{}
		}
		if _, err := AllocateMeshIP("c", "h", mustCIDR(t, "10.0.0.0/28"), taken); err == nil {
			t.Fatal("expected exhaustion error when every usable host is taken")
		}
	})
}
