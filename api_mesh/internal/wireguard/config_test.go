package wireguard

import (
	"net"
	"net/netip"
	"strings"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

func mustGenKey(t *testing.T) wgtypes.Key {
	t.Helper()
	k, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return k
}

func mustParsePrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func mustResolveUDP(t *testing.T, s string) *net.UDPAddr {
	t.Helper()
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		t.Fatalf("ParseAddrPort(%q): %v", s, err)
	}
	return net.UDPAddrFromAddrPort(ap)
}

func mustParseCIDR(t *testing.T, s string) net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("ParseCIDR(%q): %v", s, err)
	}
	return *n
}

func TestRenderConfigValid(t *testing.T) {
	priv := mustGenKey(t)
	peerKey := mustGenKey(t).PublicKey()
	cfg := Config{
		PrivateKey: priv,
		Address:    mustParsePrefix(t, "10.200.0.5/32"),
		ListenPort: 51820,
		Peers: []Peer{
			{
				PublicKey:  peerKey,
				Endpoint:   mustResolveUDP(t, "10.0.0.1:51820"),
				AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.0.0.2/32"), mustParseCIDR(t, "10.0.0.3/32")},
				KeepAlive:  25,
			},
		},
	}

	configText, err := renderConfig(cfg)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if !containsAll(configText, []string{
		"PrivateKey = " + priv.String(),
		"ListenPort = 51820",
		"PublicKey = " + peerKey.String(),
		"AllowedIPs = 10.0.0.2/32, 10.0.0.3/32",
		"PersistentKeepalive = 25",
	}) {
		t.Fatalf("config missing expected content: %s", configText)
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
