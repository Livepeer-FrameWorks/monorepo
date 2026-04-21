package xexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	fwssh "frameworks/cli/pkg/ssh"
)

// RunSSH executes the equivalent of: ssh <target> 'cd <workdir> && <cmd> <args...>'
func RunSSH(ctx context.Context, target string, cmd string, args []string, workdir string) (int, string, string, error) {
	return RunSSHWithKey(ctx, target, "", cmd, args, workdir)
}

// RunSSHWithKey executes ssh with an optional private key.
func RunSSHWithKey(ctx context.Context, target, keyPath string, cmd string, args []string, workdir string) (int, string, string, error) {
	var remoteCmd strings.Builder
	if workdir != "" {
		remoteCmd.WriteString("cd ")
		remoteCmd.WriteString(shellQuote(workdir))
		remoteCmd.WriteString(" && ")
	}
	remoteCmd.WriteString(shellQuote(cmd))
	for _, a := range args {
		remoteCmd.WriteString(" ")
		remoteCmd.WriteString(shellQuote(a))
	}

	// Reuse the shared ssh flag builder so cli/pkg/ssh and xexec always emit
	// identical options (batch mode, accept-new, timeouts, -i, etc.).
	cfg := &fwssh.ConnectionConfig{KeyPath: keyPath}
	res := fwssh.Resolution{Target: target}
	sshArgs := fwssh.BuildSSHArgs(cfg, res)
	sshArgs = append(sshArgs, target, "sh", "-lc", remoteCmd.String())

	c := exec.CommandContext(ctx, "ssh", sshArgs...)
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
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	return s
}
