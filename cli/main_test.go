package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"frameworks/cli/cmd"
)

func TestExitStatusPrintsFailuresThatCarryARemoteExitCode(t *testing.T) {
	// A failed remote command wraps *exec.ExitError, whose ExitCode() must not be mistaken for a deliberate silent exit.
	remote := exec.CommandContext(context.Background(), "sh", "-c", "exit 7")
	remoteErr := remote.Run()
	if remoteErr == nil {
		t.Fatal("expected the helper command to fail")
	}
	wrapped := fmt.Errorf("every Yugabyte node runs the new engine but the upgrade is not finalized: %w", remoteErr)

	code, message := exitStatus(wrapped)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if message != wrapped.Error() {
		t.Fatalf("message = %q, want the wrapped error text", message)
	}
}

func TestExitStatusHonoursADeliberateExitCode(t *testing.T) {
	code, message := exitStatus(fmt.Errorf("context: %w", &cmd.ExitCodeError{Code: 3, Message: "drift detected"}))
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	if message != "" {
		t.Fatalf("message = %q, want no banner", message)
	}
}

func TestExitStatusPrintsAPlainError(t *testing.T) {
	code, message := exitStatus(errors.New("boom"))
	if code != 1 || message != "boom" {
		t.Fatalf("exitStatus = (%d, %q), want (1, \"boom\")", code, message)
	}
}
