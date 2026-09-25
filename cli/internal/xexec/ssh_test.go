package xexec

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeSSHD mimics OpenSSH end to end: the client joins everything after the
// target with spaces and the server runs that one string through a shell.
const fakeSSHD = `#!/bin/sh
while [ "$#" -gt 0 ] && [ "$1" != "$FAKE_SSH_TARGET" ]; do shift; done
shift
exec sh -c "$*"
`

func useFakeSSH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte(fakeSSHD), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := sshBinary
	sshBinary = path
	t.Cleanup(func() { sshBinary = previous })
	t.Setenv("FAKE_SSH_TARGET", "edge@example")
}

func TestRunSSHWithKeyRunsWholeCommandRemotely(t *testing.T) {
	useFakeSSH(t)

	_, out, errOut, err := RunSSHWithKey(context.Background(), "edge@example", "", "echo", []string{"hello world", "it's"}, "")
	if err != nil {
		t.Fatalf("RunSSHWithKey: %v (stderr %q)", err, errOut)
	}
	if out != "hello world it's\n" {
		t.Fatalf("remote output = %q, want every argument delivered", out)
	}

	_, out, _, err = RunSSHWithKey(context.Background(), "edge@example", "", "sh", []string{"-c", "echo a; echo b"}, "")
	if err != nil {
		t.Fatalf("RunSSHWithKey script: %v", err)
	}
	if out != "a\nb\n" {
		t.Fatalf("remote script output = %q, want both lines", out)
	}
}

func TestRunSSHWithKeyHonoursWorkdir(t *testing.T) {
	useFakeSSH(t)
	dir := t.TempDir()

	_, out, _, err := RunSSHWithKey(context.Background(), "edge@example", "", "pwd", nil, dir)
	if err != nil {
		t.Fatalf("RunSSHWithKey: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(dir)
	if out != dir+"\n" && out != resolved+"\n" {
		t.Fatalf("remote pwd = %q, want %q", out, dir)
	}
}
