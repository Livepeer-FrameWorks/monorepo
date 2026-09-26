//go:build linux

package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

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
	controllers, err := mistControllerProcesses(procRoot)
	if err != nil {
		return nil, err
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

// mistControllerProcesses maps every MistController PID to its parent PID.
func mistControllerProcesses(procRoot string) (map[int]int, error) {
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
		ppid := -1
		for line := range strings.SplitSeq(string(status), "\n") {
			if value, ok := strings.CutPrefix(line, "PPid:"); ok {
				parsedPPID, parseErr := strconv.Atoi(strings.TrimSpace(value))
				if parseErr == nil {
					ppid = parsedPPID
				}
				break
			}
		}
		if ppid < 0 {
			continue
		}
		controllers[pid] = ppid
	}
	return controllers, nil
}

var mistStopGrace = 10 * time.Second

// restartMistControllerProcesses makes the supervisor (systemd Restart=always,
// s6-supervise) start a fresh MistController without needing rights over the
// unit: Helmsman shares Mist's uid, so it can signal the processes directly.
// A controller wedged in shutdown ignores SIGTERM in practice, so every
// MistController still alive after the grace period is killed.
func restartMistControllerProcesses(ctx context.Context) error {
	parents, err := mistControllerParentPIDs("/proc")
	if err != nil {
		return err
	}
	if len(parents) == 0 {
		return fmt.Errorf("no MistController process found")
	}
	return stopMistControllers(ctx, parents, func() ([]int, error) {
		controllers, scanErr := mistControllerProcesses("/proc")
		if scanErr != nil {
			return nil, scanErr
		}
		pids := make([]int, 0, len(controllers))
		for pid := range controllers {
			pids = append(pids, pid)
		}
		return pids, nil
	})
}

func stopMistControllers(ctx context.Context, parents []int, allControllers func() ([]int, error)) error {
	var signalErrs []error
	for _, pid := range parents {
		if err := unix.Kill(pid, unix.SIGTERM); err != nil && !errors.Is(err, unix.ESRCH) {
			signalErrs = append(signalErrs, fmt.Errorf("kill -TERM %d: %w", pid, err))
		}
	}
	if waitProcessesGone(ctx, parents, mistStopGrace) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pids, err := allControllers()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			signalErrs = append(signalErrs, fmt.Errorf("kill -KILL %d: %w", pid, err))
		}
	}
	if !waitProcessesGone(ctx, parents, mistStopGrace) {
		return errors.Join(append([]error{fmt.Errorf("MistController PIDs %v survived SIGKILL", parents)}, signalErrs...)...)
	}
	return nil
}

func waitProcessesGone(ctx context.Context, pids []int, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for {
		alive := false
		for _, pid := range pids {
			if processAlive(pid) {
				alive = true
				break
			}
		}
		if !alive {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// processAlive treats a zombie as gone: it no longer runs and its supervisor
// reaps it on its own schedule.
func processAlive(pid int) bool {
	if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
		return false
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(status), "\n") {
		if value, ok := strings.CutPrefix(line, "State:"); ok {
			return !strings.HasPrefix(strings.TrimSpace(value), "Z")
		}
	}
	return true
}
