//go:build linux

package updater

import "golang.org/x/sys/unix"

func exchangeDirs(src, dst string) error {
	return unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_EXCHANGE)
}
