package grpcutil

import (
	"net"
	"strings"
)

// AddrIsFQDN reports whether the address host part looks like a fully-qualified
// domain name instead of a bare service name or IP literal.
func AddrIsFQDN(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if net.ParseIP(host) != nil {
		return false
	}
	return strings.Contains(host, ".")
}
