package xexec

import (
	"bytes"
	"fmt"
	"os/exec"
)

// Run executes a command in the given working directory and returns exit code, stdout, stderr, and error.
func Run(cmd string, args []string, workdir string) (int, string, string, error) {
	c := exec.Command(cmd, args...)
	if workdir != "" {
		c.Dir = workdir
	}
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
		}
	}
	if err != nil && exit == 0 {
		// non-exit error (e.g., command not found)
		return exit, outBuf.String(), errBuf.String(), fmt.Errorf("exec error: %w", err)
	}
	return exit, outBuf.String(), errBuf.String(), err
}
