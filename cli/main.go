package main

import (
	"errors"
	"fmt"
	"frameworks/cli/cmd"
	"os"
)

func main() {
	root := cmd.NewRootCmd()
	if err := root.Execute(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
