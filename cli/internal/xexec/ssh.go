package xexec

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// RunSSH executes the equivalent of: ssh <target> 'cd <workdir> && <cmd> <args...>'
func RunSSH(target string, cmd string, args []string, workdir string) (int, string, string, error) {
	return RunSSHWithKey(target, "", cmd, args, workdir)
}

// RunSSHWithKey executes ssh with an optional private key.
func RunSSHWithKey(target, keyPath string, cmd string, args []string, workdir string) (int, string, string, error) {
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

	sshArgs := []string{"-o", "BatchMode=yes"}
	if strings.TrimSpace(keyPath) != "" {
		sshArgs = append(sshArgs, "-i", keyPath)
	}
	sshArgs = append(sshArgs, target, "sh", "-lc", remoteCmd.String())
	c := exec.Command("ssh", sshArgs...)
	var outBuf, errBuf bytes.Buffer
	c.Stdout = &outBuf
	c.Stderr = &errBuf
	err := c.Run()
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
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
