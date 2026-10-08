package coordinator

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func (a *API) proberIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	trusted := func(ip netip.Addr) bool {
		for _, prefix := range a.TrustedProxies {
			if prefix.Contains(ip.Unmap()) {
				return true
			}
		}
		return false
	}
	if !trusted(peer) {
		return peer.String()
	}
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(hops) > 32 {
		return peer.String()
	}
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer.String()
		}
		peer = hop.Unmap()
		if !trusted(peer) {
			break
		}
	}
	return peer.String()
}
