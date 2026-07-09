//go:build !linux

package updater

// s6-overlay only exists in Linux containers; elsewhere the pkill fallback in
// SignalMistUSR1 is the only direct-signal path.
func signalMistControllerProcesses() (bool, []error) {
	return false, nil
}
