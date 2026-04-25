package mesh

import (
	"encoding/base64"
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
