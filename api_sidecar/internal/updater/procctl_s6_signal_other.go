//go:build !linux

package updater

import "context"

// s6-overlay only exists in Linux containers; elsewhere the pkill fallback in
// SignalMistUSR1 is the only direct-signal path.
func signalMistControllerProcesses() (bool, []error) {
	return false, nil
}

// restartMistControllerProcesses has no /proc to find the controller tree
// here, so it kills every MistController by name and leaves the restart to
// the supervisor.
func restartMistControllerProcesses(ctx context.Context) error {
	return runCommand(ctx, "pkill", "-KILL", "-x", "MistController")
}
