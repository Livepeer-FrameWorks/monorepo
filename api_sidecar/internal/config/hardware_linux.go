//go:build linux

package config

import "golang.org/x/sys/unix"

// getMemoryBytes returns total system memory in bytes on Linux using sysinfo.
func getMemoryBytes() uint64 {
	var sysinfo unix.Sysinfo_t
	if err := unix.Sysinfo(&sysinfo); err == nil {
		return sysinfo.Totalram * uint64(sysinfo.Unit)
	}
	return 0
}
