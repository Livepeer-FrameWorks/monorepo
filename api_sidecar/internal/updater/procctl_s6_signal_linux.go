//go:build linux

package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func signalMistControllerProcesses() (bool, []error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, []error{err}
	}
	var errs []error
	signaled := false
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if readErr != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) != "MistController" {
			continue
		}
		if killErr := unix.Kill(pid, unix.SIGUSR1); killErr != nil {
			errs = append(errs, fmt.Errorf("kill -USR1 %d: %w", pid, killErr))
			continue
		}
		signaled = true
	}
	return signaled, errs
}
