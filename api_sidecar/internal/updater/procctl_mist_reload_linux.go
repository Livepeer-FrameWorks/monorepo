//go:build linux

package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

var (
	mistReloadTimeout = 90 * time.Second
	mistReloadPoll    = 250 * time.Millisecond
)

func waitMistControllerReload(ctx context.Context, controllerPath string) error {
	target, err := os.Stat(controllerPath)
	if err != nil {
		return fmt.Errorf("stat replacement MistController: %w", err)
	}
	deadline := time.Now().Add(mistReloadTimeout)
	lastState := "controller process not found"
	for {
		pids, scanErr := mistControllerParentPIDs("/proc")
		if scanErr != nil {
			lastState = scanErr.Error()
		} else if len(pids) > 0 {
			allReloaded := true
			for _, pid := range pids {
				running, statErr := os.Stat(filepath.Join("/proc", fmt.Sprint(pid), "exe"))
				if statErr != nil {
					allReloaded = false
					lastState = fmt.Sprintf("MistController PID %d executable unavailable: %v", pid, statErr)
					break
				}
				if !os.SameFile(target, running) {
					allReloaded = false
					lastState = fmt.Sprintf("MistController PID %d still runs the previous executable", pid)
					break
				}
			}
			if allReloaded {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("MistController did not re-exec %s within %s: %s", controllerPath, mistReloadTimeout, lastState)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(mistReloadPoll):
		}
	}
}
