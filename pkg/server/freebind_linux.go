package server

import (
	"syscall"
)

// ipv6FreeBind is IPV6_FREEBIND from linux/in6.h; package syscall defines only
// the IPv4 option.
const ipv6FreeBind = 0x4e

// freeBindControl sets IP_FREEBIND and IPV6_FREEBIND on the listening socket so
// bind accepts an address no interface holds yet.
func freeBindControl(network, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		if network == "tcp6" {
			sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, ipv6FreeBind, 1)
			return
		}
		sockErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_FREEBIND, 1)
	}); err != nil {
		return err
	}
	return sockErr
}
