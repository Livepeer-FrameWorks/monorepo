package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/watchdog"
)

func TestStreamDataMigrateCommandPreservesRemoteDiagnostics(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := streamDataMigrateCommand(context.Background(), ssh.NewLocalRunner(""),
		"printf 'migration progress'; echo 'verification failed' >&2; exit 3", &stdout, &stderr)
	if err == nil {
		t.Fatal("a failed command returned no error")
	}
	if got := stdout.String(); got != "migration progress\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := stderr.String(); got != "verification failed\n" {
		t.Fatalf("stderr = %q", got)
	}
}

// withDataMigrateStallWindow shortens the data-migrations stall window for one test.
func withDataMigrateStallWindow(t *testing.T, window time.Duration) {
	t.Helper()
	previous := dataMigrateStallWindow
	dataMigrateStallWindow = window
	t.Cleanup(func() { dataMigrateStallWindow = previous })
}

func runBoundedDataMigrateCommand(shellCmd string) (string, error) {
	var stdout, stderr bytes.Buffer
	err := runDataMigrateBounded(context.Background(), "purser data-migrations run example", func(ctx context.Context) error {
		_, runErr := streamDataMigrateCommand(ctx, ssh.NewLocalRunner(""), shellCmd, &stdout, &stderr)
		return runErr
	})
	return stdout.String(), err
}

// A run that keeps printing progress lines completes however long it takes in total.
func TestDataMigrateRunThatPrintsProgressOutlivesTheStallWindow(t *testing.T) {
	withDataMigrateStallWindow(t, 300*time.Millisecond)
	out, err := runBoundedDataMigrateCommand(
		`i=0; while [ $i -lt 12 ]; do echo "example/<whole-job> progress: scanned=$i"; sleep 0.1; i=$((i+1)); done; echo "example completed"`)
	if err != nil {
		t.Fatalf("a run printing progress every 100ms was cancelled within a 300ms stall window after 1.2s: %v", err)
	}
	if !strings.Contains(out, "example completed") {
		t.Fatalf("stdout = %q, want the completion line", out)
	}
}

// A run that prints nothing for the stall window is cancelled with an error naming the command and its last line.
func TestSilentDataMigrateRunIsCancelledAsStalled(t *testing.T) {
	withDataMigrateStallWindow(t, 300*time.Millisecond)
	start := time.Now()
	// exec keeps sleep the process the cancellation kills; a forked sleep would hold stdout open after sh exits.
	_, err := runBoundedDataMigrateCommand(`echo "example/<whole-job> progress: scanned=1"; exec sleep 30`)
	if err == nil {
		t.Fatal("a silent run was not cancelled")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("silent run cancelled after %s, want shortly after the 300ms stall window", elapsed)
	}
	var stall *watchdog.StallError
	if !errors.As(err, &stall) {
		t.Fatalf("error = %v, want a *watchdog.StallError", err)
	}
	for _, want := range []string{"purser data-migrations run example stalled", "no progress for 300ms", "progress: scanned=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}
