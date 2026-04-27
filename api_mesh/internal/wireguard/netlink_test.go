package wireguard

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestPrefixToAddr_IPv4Slash32(t *testing.T) {
	got := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	if got.IPNet == nil {
		t.Fatal("IPNet must not be nil")
	}
	if !got.IP.Equal(net.IPv4(10, 88, 0, 5)) {
		t.Errorf("IP = %v, want 10.88.0.5", got.IP)
	}
	ones, bits := got.Mask.Size()
	if ones != 32 || bits != 32 {
		t.Errorf("mask = /%d (bits=%d), want /32 (bits=32)", ones, bits)
	}
}

func mustAddr(t *testing.T, ipv4 string, ones int) netlink.Addr {
	t.Helper()
	ip := net.ParseIP(ipv4).To4()
	if ip == nil {
		t.Fatalf("invalid IPv4 %q", ipv4)
	}
	return netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: net.CIDRMask(ones, 32)}}
}

func TestReconcileAddresses_EmptyAddsDesired(t *testing.T) {
	desired := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	del, add := reconcileAddresses(nil, desired)
	if len(del) != 0 {
		t.Errorf("expected no deletions, got %d", len(del))
	}
	if !add {
		t.Error("desired must be added when existing is empty")
	}
}

func TestReconcileAddresses_DesiredAlreadyPresent(t *testing.T) {
	desired := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	existing := []netlink.Addr{mustAddr(t, "10.88.0.5", 32)}
	del, add := reconcileAddresses(existing, desired)
	if len(del) != 0 {
		t.Errorf("expected no deletions, got %d", len(del))
	}
	if add {
		t.Error("desired must not be re-added when already present")
	}
}

func TestReconcileAddresses_DeletesStale(t *testing.T) {
	desired := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	stale := mustAddr(t, "10.88.0.4", 32)
	existing := []netlink.Addr{stale}
	del, add := reconcileAddresses(existing, desired)
	if len(del) != 1 || !del[0].IP.Equal(stale.IP) {
		t.Errorf("expected single deletion of %s, got %+v", stale.IPNet, del)
	}
	if !add {
		t.Error("desired must be added when only stale addresses exist")
	}
}

func TestReconcileAddresses_KeepsDesiredDeletesOthers(t *testing.T) {
	desired := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	stale1 := mustAddr(t, "10.88.0.4", 32)
	stale2 := mustAddr(t, "10.88.0.6", 32)
	existing := []netlink.Addr{stale1, mustAddr(t, "10.88.0.5", 32), stale2}
	del, add := reconcileAddresses(existing, desired)
	if len(del) != 2 {
		t.Fatalf("expected 2 deletions, got %d", len(del))
	}
	if !del[0].IP.Equal(stale1.IP) || !del[1].IP.Equal(stale2.IP) {
		t.Errorf("deletion order/contents wrong: %+v", del)
	}
	if add {
		t.Error("desired must not be re-added when present alongside stale entries")
	}
}

func TestReconcileAddresses_RejectsWiderMaskAsStale(t *testing.T) {
	// Same IP, different mask must be treated as a stale entry to delete,
	// not an in-place match — otherwise a /24 already on the link would
	// silently mask the desired /32 and capture peer traffic.
	desired := prefixToAddr(mustParsePrefix(t, "10.88.0.5/32"))
	wider := mustAddr(t, "10.88.0.5", 24)
	del, add := reconcileAddresses([]netlink.Addr{wider}, desired)
	if len(del) != 1 {
		t.Fatalf("wider mask should be deleted, got %d deletions", len(del))
	}
	if !add {
		t.Error("desired /32 must be added after deleting wider mask")
	}
}

func TestAddrEqual(t *testing.T) {
	a := netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(10, 88, 0, 5).To4(), Mask: net.CIDRMask(32, 32)}}
	same := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(10, 88, 0, 5).To4(), Mask: net.CIDRMask(32, 32)}}
	differentIP := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(10, 88, 0, 6).To4(), Mask: net.CIDRMask(32, 32)}}
	differentMask := &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(10, 88, 0, 5).To4(), Mask: net.CIDRMask(24, 32)}}

	if !addrEqual(a, same) {
		t.Error("identical addresses should compare equal")
	}
	if addrEqual(a, differentIP) {
		t.Error("addresses with different IPs should not compare equal")
	}
	if addrEqual(a, differentMask) {
		t.Error("addresses with different masks should not compare equal")
	}
	if addrEqual(a, &netlink.Addr{}) {
		t.Error("empty Addr.IPNet should not compare equal to populated Addr")
	}
}

func TestPeerRoutes(t *testing.T) {
	routes := peerRoutes(mustParsePrefix(t, "10.88.0.5/32"), []Peer{
		{
			AllowedIPs: []net.IPNet{mustParseCIDR(t, "10.88.0.6/32")},
		},
		{
			AllowedIPs: []net.IPNet{
				mustParseCIDR(t, "10.88.0.7/32"),
				mustParseCIDR(t, "10.88.0.6/32"),
				mustParseCIDR(t, "10.88.0.5/32"),
			},
		},
	})

	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(routes))
	}
	if routes[0].String() != "10.88.0.6/32" || routes[1].String() != "10.88.0.7/32" {
		t.Errorf("routes = %v, want [10.88.0.6/32 10.88.0.7/32]", routes)
	}
}
