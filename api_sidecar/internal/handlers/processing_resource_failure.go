package handlers

import (
	"errors"
	"fmt"
)

// Processing processes use code 1 for transient runtime failures. A signal
// kill also says nothing about the source's validity, even with restart disabled.
func retryableProcessingExit(evt ProcessExitEvent) bool {
	var oom *processingOOMError
	if errors.As(processingExitError(evt), &oom) {
		return true
	}
	return isCriticalProcess(evt) && (evt.ExitCode == -9 || evt.ExitCode == 137 || evt.ExitCode == 1)
}

type processingResourceError struct{ event ProcessExitEvent }

func (e *processingResourceError) Error() string {
	return fmt.Sprintf("%s process requires a fresh attempt (exit %d): %s", e.event.ProcessType, e.event.ExitCode, e.event.Reason)
}

// Terminal triggers can arrive before their queued PROCESS_EXIT is consumed.
// Retaining the exit beside the listener prevents that ordering from turning
// a node failure into a permanent missing-rendition failure.
func processingResourceFailure(stream string, ignored map[string]int) error {
	processExitListenersMu.Lock()
	defer processExitListenersMu.Unlock()
	for _, evt := range processResourceFailures[stream] {
		if !shouldIgnoreProcessExit(evt, ignored) {
			return processingExitError(evt)
		}
	}
	return nil
}

func clearProcessingResourceFailures(stream string) {
	processExitListenersMu.Lock()
	delete(processResourceFailures, stream)
	processExitListenersMu.Unlock()
}
