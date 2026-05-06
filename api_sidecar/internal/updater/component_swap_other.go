//go:build !linux && !darwin

package updater

import "fmt"

func exchangeDirs(src, dst string) error {
	return fmt.Errorf("atomic directory exchange is unsupported on this platform for %s and %s", src, dst)
}
