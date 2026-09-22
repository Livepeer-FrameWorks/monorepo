package main

import (
	"context"
	"errors"
	"fmt"
	"frameworks/cli/cmd"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	root := cmd.NewRootCmd()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		code, message := exitStatus(err)
		if message != "" {
			fmt.Fprintln(os.Stderr, message)
		}
		os.Exit(code)
	}
}

// exitStatus maps a command failure to a process exit code and the banner to print, if any. It matches
// *cmd.ExitCodeError by its concrete type rather than by an ExitCode() interface: every failed remote command wraps an
// *exec.ExitError, which also carries ExitCode(), so a structural match would exit with the remote command's status and
// swallow the message of any failure that reached the CLI through SSH.
func exitStatus(err error) (int, string) {
	var exitErr *cmd.ExitCodeError
	if errors.As(err, &exitErr) {
		return exitErr.Code, ""
	}
	return 1, err.Error()
}
