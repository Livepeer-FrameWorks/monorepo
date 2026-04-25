package wireguard

import (
	"net"
	"strings"
	"testing"
)

func validBase(t *testing.T) Config {
	t.Helper()
	priv := mustGenKey(t)
	peer := mustGenKey(t).PublicKey()
	return Config{
		PrivateKey: priv,
		Address:    mustParsePrefix(t, "10.88.0.5/32"),
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  peer,
				Endpoint:   mustResolveUDP(t, "10.0.0.1:51820"),
				AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.88.0.6/32")},
				KeepAlive:  25,
			},
		},
	}
}

func TestValidateForApply_Valid(t *testing.T) {
	if err := ValidateForApply(validBase(t)); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateForApply_PortOutOfRange(t *testing.T) {
	for _, port := range []int{0, -1, 65536} {
		cfg := validBase(t)
		cfg.ListenPort = port
		if err := ValidateForApply(cfg); err == nil {
			t.Fatalf("port %d should fail", port)
		}
	}
}

func TestValidateForApply_PortBoundaries(t *testing.T) {
	for _, port := range []int{1, 65535} {
		cfg := validBase(t)
		cfg.ListenPort = port
		if err := ValidateForApply(cfg); err != nil {
			t.Fatalf("port %d should pass: %v", port, err)
		}
	}
}

func TestValidateForApply_SelfPeer(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].PublicKey = cfg.PrivateKey.PublicKey()
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "matches self") {
		t.Fatalf("expected self-peer rejection, got: %v", err)
	}
}

func TestValidateForApply_DuplicatePeer(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers = append(cfg.Peers, cfg.Peers[0])
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-peer rejection, got: %v", err)
	}
}

func TestValidateForApply_MissingEndpoint(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].Endpoint = nil
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "endpoint is required") {
		t.Fatalf("expected missing-endpoint rejection, got: %v", err)
	}
}

func TestValidateForApply_NonSlash32AllowedIP(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].AllowedIPs = []net.IPNet{mustParseCIDR(t, "10.88.0.0/24")}
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "must be /32") {
		t.Fatalf("expected /32 rejection, got: %v", err)
	}
}

func TestValidateForApply_IPv6AllowedIPRejected(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].AllowedIPs = []net.IPNet{mustParseCIDR(t, "fd00::1/128")}
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "must be IPv4") {
		t.Fatalf("expected IPv4-only rejection, got: %v", err)
	}
}

func TestValidateForApply_EmptyAllowedIPs(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].AllowedIPs = nil
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "allowed_ips is empty") {
		t.Fatalf("expected empty allowed_ips rejection, got: %v", err)
	}
}

func TestValidateForApply_NegativeKeepAlive(t *testing.T) {
	cfg := validBase(t)
	cfg.Peers[0].KeepAlive = -1
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "keep_alive") {
		t.Fatalf("expected keep_alive rejection, got: %v", err)
	}
}

func TestValidateForApply_ZeroPrivateKey(t *testing.T) {
	cfg := validBase(t)
	cfg.PrivateKey = [32]byte{}
	err := ValidateForApply(cfg)
	if err == nil || !strings.Contains(err.Error(), "private key is unset") {
		t.Fatalf("expected zero-private-key rejection, got: %v", err)
	}
}
