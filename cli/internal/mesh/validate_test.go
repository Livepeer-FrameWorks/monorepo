package mesh

import (
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// validHost builds a host that passes every ValidateIdentity rule, using a
// real generated keypair so DerivePublicKey agrees with WireguardPublicKey.
func validHost(t *testing.T, ip string) inventory.Host {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return inventory.Host{
		WireguardIP:         ip,
		WireguardPort:       51820,
		WireguardPublicKey:  pub,
		WireguardPrivateKey: priv,
	}
}

func TestValidateIdentity(t *testing.T) {
	t.Parallel()

	t.Run("empty hostNames is a no-op", func(t *testing.T) {
		t.Parallel()
		if err := ValidateIdentity(&inventory.Manifest{}, nil); err != nil {
			t.Fatalf("expected nil for empty hostNames, got %v", err)
		}
	})

	t.Run("nil manifest errors", func(t *testing.T) {
		t.Parallel()
		if err := ValidateIdentity(nil, []string{"a"}); err == nil {
			t.Fatal("expected error for nil manifest")
		}
	})

	t.Run("fully valid manifest passes", func(t *testing.T) {
		t.Parallel()
		m := &inventory.Manifest{
			WireGuard: &inventory.WireGuardConfig{Enabled: true, MeshCIDR: "10.88.0.0/24"},
			Hosts:     map[string]inventory.Host{"a": validHost(t, "10.88.0.1")},
		}
		if err := ValidateIdentity(m, []string{"a"}); err != nil {
			t.Fatalf("expected valid manifest to pass, got %v", err)
		}
	})

	// Each case mutates one rule on an otherwise-valid manifest and asserts the
	// resulting error mentions that rule.
	cases := []struct {
		name      string
		mutate    func(t *testing.T, m *inventory.Manifest)
		wantInErr string
	}{
		{
			name:      "wireguard disabled",
			mutate:    func(_ *testing.T, m *inventory.Manifest) { m.WireGuard.Enabled = false },
			wantInErr: "wireguard.enabled must be true",
		},
		{
			name:      "missing mesh_cidr",
			mutate:    func(_ *testing.T, m *inventory.Manifest) { m.WireGuard.MeshCIDR = "" },
			wantInErr: "mesh_cidr is required",
		},
		{
			name:      "invalid mesh_cidr",
			mutate:    func(_ *testing.T, m *inventory.Manifest) { m.WireGuard.MeshCIDR = "not-a-cidr" },
			wantInErr: "is invalid",
		},
		{
			name:      "non-ipv4 mesh_cidr",
			mutate:    func(_ *testing.T, m *inventory.Manifest) { m.WireGuard.MeshCIDR = "fd00::/64" },
			wantInErr: "must be IPv4",
		},
		{
			name:      "undeclared host",
			mutate:    func(_ *testing.T, m *inventory.Manifest) { delete(m.Hosts, "a") },
			wantInErr: "is not declared",
		},
		{
			name: "missing wireguard_ip",
			mutate: func(_ *testing.T, m *inventory.Manifest) {
				h := m.Hosts["a"]
				h.WireguardIP = ""
				m.Hosts["a"] = h
			},
			wantInErr: "wireguard_ip is required",
		},
		{
			name: "ip outside cidr",
			mutate: func(_ *testing.T, m *inventory.Manifest) {
				h := m.Hosts["a"]
				h.WireguardIP = "192.168.1.1"
				m.Hosts["a"] = h
			},
			wantInErr: "is outside",
		},
		{
			name: "bad port",
			mutate: func(_ *testing.T, m *inventory.Manifest) {
				h := m.Hosts["a"]
				h.WireguardPort = 0
				m.Hosts["a"] = h
			},
			wantInErr: "wireguard_port must be 1-65535",
		},
		{
			name: "invalid public key",
			mutate: func(_ *testing.T, m *inventory.Manifest) {
				h := m.Hosts["a"]
				h.WireguardPublicKey = "not-base64-key"
				m.Hosts["a"] = h
			},
			wantInErr: "wireguard_public_key",
		},
		{
			name: "public key does not match private key",
			mutate: func(t *testing.T, m *inventory.Manifest) {
				_, otherPub, err := GenerateKeyPair()
				if err != nil {
					t.Fatalf("GenerateKeyPair: %v", err)
				}
				h := m.Hosts["a"]
				h.WireguardPublicKey = otherPub
				m.Hosts["a"] = h
			},
			wantInErr: "does not match",
		},
		{
			name: "unmanaged key without key file",
			mutate: func(_ *testing.T, m *inventory.Manifest) {
				h := m.Hosts["a"]
				managed := false
				h.WireguardPrivateKeyManaged = &managed
				h.WireguardPrivateKeyFile = ""
				m.Hosts["a"] = h
			},
			wantInErr: "wireguard_private_key_file is empty",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &inventory.Manifest{
				WireGuard: &inventory.WireGuardConfig{Enabled: true, MeshCIDR: "10.88.0.0/24"},
				Hosts:     map[string]inventory.Host{"a": validHost(t, "10.88.0.1")},
			}
			tc.mutate(t, m)
			err := ValidateIdentity(m, []string{"a"})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantInErr)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantInErr)
			}
		})
	}
}

func TestValidateIdentityDuplicateIP(t *testing.T) {
	t.Parallel()
	m := &inventory.Manifest{
		WireGuard: &inventory.WireGuardConfig{Enabled: true, MeshCIDR: "10.88.0.0/24"},
		Hosts: map[string]inventory.Host{
			"a": validHost(t, "10.88.0.1"),
			"b": validHost(t, "10.88.0.1"), // same IP as a
		},
	}
	err := ValidateIdentity(m, []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "share wireguard_ip") {
		t.Fatalf("expected duplicate-ip error, got %v", err)
	}
}

func TestValidateBase64Key(t *testing.T) {
	t.Parallel()
	_, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	cases := []struct {
		name      string
		key       string
		wantErr   bool
		wantInErr string
	}{
		{name: "empty", key: "", wantErr: true, wantInErr: "is required"},
		{name: "whitespace only", key: "   ", wantErr: true, wantInErr: "is required"},
		{name: "garbage", key: "definitely-not-a-key", wantErr: true, wantInErr: "invalid wireguard key"},
		{name: "valid", key: pub, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateBase64Key(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.key)
				}
				if tc.wantInErr != "" && !strings.Contains(err.Error(), tc.wantInErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantInErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
