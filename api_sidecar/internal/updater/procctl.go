package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ServiceController abstracts the supervisor that owns the MistServer and
// Caddy processes: systemd on native Linux, launchd on native macOS, or
// s6-overlay inside the single-image edge container. Component swaps go
// through it so the updater never assumes a particular init system.
type ServiceController interface {
	// RestartCaddy restarts the Caddy process so a swapped binary takes effect.
	RestartCaddy(ctx context.Context) error
	// SignalMistUSR1 delivers SIGUSR1 to MistController for an in-place
	// rolling reload after a binary swap.
	SignalMistUSR1(ctx context.Context) error
}

var (
	serviceControllerMu sync.Mutex
	serviceController   ServiceController
)

func currentServiceController() ServiceController {
	serviceControllerMu.Lock()
	defer serviceControllerMu.Unlock()
	if serviceController == nil {
		serviceController = detectServiceController()
	}
	return serviceController
}

// SetServiceControllerForTest overrides supervisor detection and returns a
// restore function.
func SetServiceControllerForTest(ctrl ServiceController) func() {
	serviceControllerMu.Lock()
	prev := serviceController
	serviceController = ctrl
	serviceControllerMu.Unlock()
	return func() {
		serviceControllerMu.Lock()
		serviceController = prev
		serviceControllerMu.Unlock()
	}
}

func detectServiceController() ServiceController {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("HELMSMAN_SUPERVISOR"))) {
	case "s6":
		return s6Controller{}
	case "systemd":
		return systemdController{}
	case "launchd":
		return launchdController{}
	}
	if s6SupervisionPresent() {
		return s6Controller{}
	}
	if runtime.GOOS == "darwin" {
		return launchdController{}
	}
	return systemdController{}
}

func s6SupervisionPresent() bool {
	for _, path := range []string{"/run/s6/basedir", "/run/service"} {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func firstCommand(ctx context.Context, first ...string) error {
	if len(first)%4 != 0 {
		return fmt.Errorf("invalid command list")
	}
	var errs []error
	for i := 0; i < len(first); i += 4 {
		if err := runCommand(ctx, first[i], first[i+1:i+4]...); err == nil {
			return nil
		} else {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
