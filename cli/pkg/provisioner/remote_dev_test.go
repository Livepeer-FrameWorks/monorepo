package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRemoteDevRunPreservesRemoteEnvironmentAndArguments(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	bin := t.TempDir()
	// Unpack the remote shell argument without executing it or contacting a host.
	stub := "#!/bin/bash\nfor arg; do last=$arg; done\neval \"set -- $last\"\nprintf '%s' \"$3\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "scripts/remote-dev.sh", "run", "test-slot", "printf", "a b;$HOME")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "REMOTE_DEV_HOST=example.invalid", "REMOTE_DEV_PORT=22", "REMOTE_DEV_USER=tester")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("render remote command: %v: %s", err, out)
	}
	for _, want := range []string{
		`export PATH="$PNPM_HOME:$HOME/go/bin:$CARGO_HOME/bin:$PATH"`,
		`mkdir -p "$GOCACHE" "$GOMODCACHE" "$PNPM_HOME" "$PNPM_STORE_DIR" "$CARGO_HOME"`,
		`exec printf a\ b\;\$HOME`,
		`flock -n "$lock_fd"`,
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("remote command lacks %q: %s", want, out)
		}
	}
}

func TestRemoteDevHelpNeedsNoSecrets(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	cmd := exec.CommandContext(t.Context(), "bash", "scripts/remote-dev.sh", "--help")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "FRAMEWORKS_GITOPS_DIR="+t.TempDir(), "REMOTE_DEV_HOST=", "REMOTE_DEV_PORT=")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Usage:") {
		t.Fatalf("help requires deployment secrets: %v: %s", err, out)
	}
}
