//go:build linux

package updater

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func signalMistControllerProcesses() (bool, []error) {
	pids, err := mistControllerParentPIDs("/proc")
	if err != nil {
		return false, []error{err}
	}
	var errs []error
	signaled := false
	for _, pid := range pids {
		if killErr := unix.Kill(pid, unix.SIGUSR1); killErr != nil {
			errs = append(errs, fmt.Errorf("kill -USR1 %d: %w", pid, killErr))
			continue
		}
		signaled = true
	}
	return signaled, errs
}

func mistControllerParentPIDs(procRoot string) ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	controllers := map[int]int{}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		comm, readErr := os.ReadFile(filepath.Join(procRoot, entry.Name(), "comm"))
		if readErr != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) != "MistController" {
			continue
		}
		status, readErr := os.ReadFile(filepath.Join(procRoot, entry.Name(), "status"))
		if readErr != nil {
			continue
		}
		ppid := 0
		for line := range strings.SplitSeq(string(status), "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				ppid, _ = strconv.Atoi(strings.TrimSpace(value))
				break
			}
		}
		controllers[pid] = ppid
	}
	parents := make([]int, 0, len(controllers))
	for pid, ppid := range controllers {
		if _, childOfController := controllers[ppid]; !childOfController {
			parents = append(parents, pid)
		}
	}
	slices.Sort(parents)
	return parents, nil
}
