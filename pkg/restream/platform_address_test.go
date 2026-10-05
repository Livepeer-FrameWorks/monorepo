package restream

import (
	"net"
	"testing"
)

func TestPlatformAddressesRefusesMeshRoutedPrivateAddresses(t *testing.T) {
	wg0 := net.Interface{Name: "wg0", Flags: net.FlagUp | net.FlagPointToPoint}
	eth0 := net.Interface{Name: "eth0", Flags: net.FlagUp | net.FlagBroadcast, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1}}
	routes := map[string]net.Interface{
		"10.89.0.7":     wg0,  // another platform host on the staging mesh
		"10.88.12.3":    wg0,  // a mesh with a different CIDR
		"192.168.10.40": eth0, // the customer's isolated LAN
		"8.8.8.8":       wg0,  // public, behind a tunnel default route
	}
	addrs := platformAddresses{
		interfaceAddrs: func() ([]net.Addr, error) {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.10.5"), Mask: net.CIDRMask(24, 32)}}, nil
		},
		egressInterface: func(ip net.IP) (net.Interface, bool) {
			iface, ok := routes[ip.String()]
			return iface, ok
		},
	}
	for addr, want := range map[string]bool{
		"10.89.0.7":     true,
		"10.88.12.3":    true,
		"192.168.10.5":  true, // this host
		"192.168.10.40": false,
		"8.8.8.8":       false,
		"172.16.4.4":    false, // no route known
	} {
		if got := addrs.contains(net.ParseIP(addr)); got != want {
			t.Errorf("contains(%s) = %v, want %v", addr, got, want)
		}
	}

	policy := WebhookDestinationPolicy(true)
	policy.PlatformAddress = addrs.contains
	if err := policy.ValidateIP(net.ParseIP("10.89.0.7")); err == nil {
		t.Error("private destinations allowed must still refuse a mesh address")
	}
	if err := policy.ValidateIP(net.ParseIP("192.168.10.40")); err != nil {
		t.Errorf("private destinations allowed must admit the customer LAN: %v", err)
	}
}
