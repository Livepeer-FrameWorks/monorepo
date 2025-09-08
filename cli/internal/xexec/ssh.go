package xexec

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// RunSSH executes the equivalent of: ssh <target> 'cd <workdir> && <cmd> <args...>'
func RunSSH(target string, cmd string, args []string, workdir string) (int, string, string, error) {
	// Build remote shell command
	// Use sh -lc to have a login-like shell with PATH and && chaining
	var remoteCmd strings.Builder
	if workdir != "" {
		remoteCmd.WriteString("cd ")
		// naive escaping for spaces
		remoteCmd.WriteString(shellQuote(workdir))
		remoteCmd.WriteString(" && ")
	}
	remoteCmd.WriteString(shellQuote(cmd))
	for _, a := range args {
		remoteCmd.WriteString(" ")
		remoteCmd.WriteString(shellQuote(a))
	}

	c := exec.Command("ssh", "-o", "BatchMode=yes", target, "sh", "-lc", remoteCmd.String())
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
		return exit, outBuf.String(), errBuf.String(), fmt.Errorf("ssh exec error: %w", err)
	}
	return exit, outBuf.String(), errBuf.String(), err
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.ContainsAny(s, " \t\n\"'\\$`|&;<>(){}[]*") {
		// simple single-quote wrapping with escape for single quotes
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	return s
}
