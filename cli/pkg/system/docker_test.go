package system

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDockerEnv puts stub `docker` and `sudo` binaries on PATH. The docker stub
// answers `docker version` with daemonExit and echoes every other invocation
// prefixed with "user:"; sudo strips -n and echoes "sudo:" plus the arguments.
func fakeDockerEnv(t *testing.T, daemonExit string) []string {
	t.Helper()
	dir := t.TempDir()
	docker := "#!/bin/sh\nif [ \"$1\" = version ]; then exit " + daemonExit + "; fi\necho \"user:$*\"\n"
	sudo := "#!/bin/sh\n[ \"$1\" = -n ] && shift\nshift\necho \"sudo:$*\"\n"
	for name, body := range map[string]string{"docker": docker, "sudo": sudo} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
}

func runShell(t *testing.T, env []string, script string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c %q: %v\n%s", script, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDockerCommandRunsAsUserWhenDaemonReachable(t *testing.T) {
	env := fakeDockerEnv(t, "0")
	got := runShell(t, env, DockerCommand("compose -f '/opt/frameworks/x/docker-compose.yml' up -d"))
	if got != "user:compose -f /opt/frameworks/x/docker-compose.yml up -d" {
		t.Fatalf("output = %q, want a single user invocation", got)
	}
}

func TestDockerCommandFallsBackToSudoOnce(t *testing.T) {
	env := fakeDockerEnv(t, "1")
	got := runShell(t, env, DockerCommand("restart frameworks-foghorn"))
	if got != "sudo:restart frameworks-foghorn" {
		t.Fatalf("output = %q, want exactly one sudo invocation", got)
	}
}

func TestDockerCommandPipeAppliesToSelectedBranch(t *testing.T) {
	env := fakeDockerEnv(t, "1")
	got := runShell(t, env, DockerCommand("info --format '{{json .Runtimes}}'")+" 2>/dev/null | tr a-z A-Z")
	if got != "SUDO:INFO --FORMAT {{JSON .RUNTIMES}}" {
		t.Fatalf("output = %q, want the sudo branch piped through tr", got)
	}
}
