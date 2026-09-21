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
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
