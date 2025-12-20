//go:build darwin

package config

import "golang.org/x/sys/unix"

// getMemoryBytes returns total system memory in bytes on Darwin using sysctl.
func getMemoryBytes() uint64 {
	memsize, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return memsize
}
