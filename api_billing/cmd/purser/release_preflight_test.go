package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestReleasePreflightRejectsArgumentsBeforeStartup(t *testing.T) {
	if os.Getenv("FW_PREFLIGHT_ARG_TEST") == "1" {
		os.Args = []string{"purser", "release-preflight", "--unexpected"}
		main()
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleasePreflightRejectsArgumentsBeforeStartup$")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "FW_PREFLIGHT_ARG_TEST=1"}
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("malformed preview must exit with usage status 2 before normal startup; got %v", err)
	}
}
