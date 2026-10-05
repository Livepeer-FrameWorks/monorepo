package restream

import (
	"net"
)

// IsPlatformAddress reports whether ip belongs to the platform rather than to
// a customer network: an address assigned to this host (every service bound
// to all interfaces answers on it), or a non-public address the kernel routes
// through a layer-3 tunnel interface. The Privateer WireGuard mesh is such a
// tunnel, and every *.internal service name resolves into it, so a mesh
// address is refused whatever CIDR the cluster's mesh uses. A customer
// network reached over a site-to-site tunnel from a platform host is refused
// for the same reason; public addresses are judged by the rest of the policy
// even when the default route is a tunnel.
func IsPlatformAddress(ip net.IP) bool {
	return systemPlatformAddresses.contains(ip)
}

// platformAddresses holds the host lookups IsPlatformAddress uses, so tests
// can stand in for interfaces and routes the test host does not have.
type platformAddresses struct {
	interfaceAddrs  func() ([]net.Addr, error)
	egressInterface func(net.IP) (net.Interface, bool)
}

var systemPlatformAddresses = platformAddresses{
	interfaceAddrs:  net.InterfaceAddrs,
	egressInterface: kernelEgressInterface,
}

func (p platformAddresses) contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if addrs, err := p.interfaceAddrs(); err == nil {
		for _, addr := range addrs {
			if prefix, ok := addr.(*net.IPNet); ok && prefix.IP.Equal(ip) {
				return true
			}
		}
	}
	if isPublicUnicast(ip) {
		return false
	}
	iface, ok := p.egressInterface(ip)
	return ok && isTunnelInterface(iface)
}

// isPublicUnicast reports addresses that are neither private nor in a
// non-public range; only these can be a legitimate destination behind a
// default route that happens to be a tunnel.
func isPublicUnicast(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !containsIP(nonPublicRanges, ip)
}

// isTunnelInterface reports a point-to-point interface with no link-layer
// address, which is how WireGuard (and other layer-3 tunnels) present.
func isTunnelInterface(iface net.Interface) bool {
	return iface.Flags&net.FlagPointToPoint != 0 && len(iface.HardwareAddr) == 0
}

// kernelEgressInterface asks the kernel which interface it would send to ip
// from. Connecting a UDP socket performs the route lookup and sends nothing.
func kernelEgressInterface(ip net.IP) (net.Interface, bool) {
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: ip, Port: 9})
	if err != nil {
		return net.Interface{}, false
	}
	local, ok := conn.LocalAddr().(*net.UDPAddr)
	_ = conn.Close()
	if !ok || local == nil {
		return net.Interface{}, false
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return net.Interface{}, false
	}
	for _, iface := range ifaces {
		addrs, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, addr := range addrs {
			if prefix, ok := addr.(*net.IPNet); ok && prefix.IP.Equal(local.IP) {
				return iface, true
			}
		}
	}
	return net.Interface{}, false
}
