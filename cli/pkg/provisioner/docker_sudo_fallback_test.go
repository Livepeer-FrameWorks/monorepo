package provisioner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/system"
)

// sudoOnlyDockerEnv models a host whose SSH user cannot reach the Docker
// socket: `docker` fails every invocation, and `sudo -n docker` answers with
// the given stdout.
func sudoOnlyDockerEnv(t *testing.T, sudoStdout string) []string {
	t.Helper()
	dir := t.TempDir()
	stubs := map[string]string{
		"docker": "#!/bin/sh\necho 'permission denied while trying to connect to the Docker daemon socket' >&2\nexit 1\n",
		"sudo":   "#!/bin/sh\n[ \"$1\" = -n ] && [ \"$2\" = docker ] || exit 1\nprintf '%s\\n' '" + sudoStdout + "'\n",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
}

func runProbe(env []string, script string) (string, int) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", script)
	cmd.Env = env
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		return err.Error(), -1
	}
	return string(out), 0
}

func TestEdgeNVIDIARuntimeProbeUsesSudoWhenSocketDenied(t *testing.T) {
	env := sudoOnlyDockerEnv(t, `{"io.containerd.runc.v2":{},"nvidia":{"path":"nvidia-container-runtime"}}`)
	if out, code := runProbe(env, edgeNVIDIARuntimeProbe); code != 0 {
		t.Fatalf("probe exit = %d (%q), want 0: the NVIDIA runtime is only visible through sudo", code, out)
	}
	env = sudoOnlyDockerEnv(t, `{"runc":{}}`)
	if _, code := runProbe(env, edgeNVIDIARuntimeProbe); code == 0 {
		t.Fatal("probe exit = 0 without an NVIDIA runtime")
	}
}

func TestEdgeContainerNamesProbeUsesSudoWhenSocketDenied(t *testing.T) {
	env := sudoOnlyDockerEnv(t, "frameworks-edge")
	out, code := runProbe(env, edgeContainerNamesProbe)
	if code != 0 || strings.TrimSpace(out) != "frameworks-edge" {
		t.Fatalf("probe = %q exit %d, want frameworks-edge via sudo", out, code)
	}
}

func TestEdgeComposeStateProbeUsesSudoWhenSocketDenied(t *testing.T) {
	env := sudoOnlyDockerEnv(t, `{"Name":"frameworks-edge","State":"running"}`)
	out, code := runProbe(env, edgeComposeStateProbe)
	if code != 0 || !strings.Contains(out, "frameworks-edge") {
		t.Fatalf("probe = %q exit %d, want compose state via sudo", out, code)
	}
}

func TestCleanupAttemptsWrapDockerWithSudoFallback(t *testing.T) {
	docker := []string{
		system.DockerCommand("compose stop foghorn"),
		system.DockerCommand("stop frameworks-foghorn"),
		system.DockerCommand("rm -f frameworks-foghorn"),
	}
	systemd := []string{"systemctl stop frameworks-foghorn", "systemctl kill frameworks-foghorn"}
	for mode, want := range map[string][]string{
		"docker": docker,
		"native": systemd,
		"":       append(append([]string{}, docker...), systemd...),
	} {
		got := cleanupAttempts(mode, "foghorn")
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("mode %q attempts = %q, want %q", mode, got, want)
		}
	}
}
