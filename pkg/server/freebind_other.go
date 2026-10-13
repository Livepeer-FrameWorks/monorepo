//go:build !linux

package server

import "syscall"

// freeBindControl is nil off Linux: the bind needs the address to be assigned.
var freeBindControl func(network, address string, c syscall.RawConn) error
