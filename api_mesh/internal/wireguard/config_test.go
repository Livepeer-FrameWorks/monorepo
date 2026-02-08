package wireguard

import (
	"strings"
	"testing"
)

func TestRenderConfigValid(t *testing.T) {
	cfg := Config{
		PrivateKey: "privkey",
		Address:    "10.200.0.5/32",
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  "peer1",
				Endpoint:   "10.0.0.1:51820",
				AllowedIPs: []string{"10.0.0.2/32", "10.0.0.3/32"},
				KeepAlive:  25,
			},
		},
	}

	configText, err := renderConfig(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !containsAll(configText, []string{
		"PrivateKey = privkey",
		"ListenPort = 51820",
		"PublicKey = peer1",
		"AllowedIPs = 10.0.0.2/32, 10.0.0.3/32",
		"PersistentKeepalive = 25",
	}) {
		t.Fatalf("config missing expected content: %s", configText)
	}
}

func TestRenderConfigRejectsMalformedPeer(t *testing.T) {
	cfg := Config{
		PrivateKey: "privkey",
		Address:    "10.200.0.5/32",
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  "",
				AllowedIPs: []string{"10.0.0.2/32"},
				KeepAlive:  25,
			},
		},
	}

	_, err := renderConfig(cfg)
	if err == nil {
		t.Fatal("expected error for malformed peer, got nil")
	}
}

func TestValidateConfig_PortBoundary(t *testing.T) {
	base := Config{
		PrivateKey: "privkey",
		Address:    "10.0.0.1/32",
		Peers:      nil,
	}

	// port=0 should fail
	base.ListenPort = 0
	if err := validateConfig(base); err == nil {
		t.Fatal("port 0 should fail validation")
	}

	// port=1 should pass
	base.ListenPort = 1
	if err := validateConfig(base); err != nil {
		t.Fatalf("port 1 should pass, got: %v", err)
	}
}

func TestValidateConfig_KeepaliveBoundary(t *testing.T) {
	base := Config{
		PrivateKey: "privkey",
		Address:    "10.0.0.1/32",
		ListenPort: 51820,
		Peers: []Peer{{
			PublicKey:  "pubkey",
			AllowedIPs: []string{"10.0.0.2/32"},
		}},
	}

	// keepalive=-1 should fail
	base.Peers[0].KeepAlive = -1
	if err := validateConfig(base); err == nil {
		t.Fatal("keepalive -1 should fail validation")
	}

	// keepalive=0 should pass
	base.Peers[0].KeepAlive = 0
	if err := validateConfig(base); err != nil {
		t.Fatalf("keepalive 0 should pass, got: %v", err)
	}
}

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
