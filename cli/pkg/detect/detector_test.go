package detect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/system"
)

// fakeRunner records calls and returns scripted responses per command prefix.
type fakeRunner struct {
	responses []fakeResponse
	calls     []string
}

type fakeResponse struct {
	matchPrefix string
	exitCode    int
	stdout      string
	stderr      string
	err         error
}

func (f *fakeRunner) runSSH(_ context.Context, cmd string) (int, string, string, error) {
	f.calls = append(f.calls, cmd)
	for _, r := range f.responses {
		if r.matchPrefix == "" || startsWith(cmd, r.matchPrefix) || startsWith("docker "+dockerUserBranch(cmd), r.matchPrefix) {
			return r.exitCode, r.stdout, r.stderr, r.err
		}
	}
	return -1, "", "no response configured", nil
}

// dockerUserBranch returns the docker arguments of a system.DockerCommand
// wrapper, or "" when cmd is not one.
func dockerUserBranch(cmd string) string {
	const head = "if docker version >/dev/null 2>&1; then docker "
	rest, ok := strings.CutPrefix(cmd, head)
	if !ok {
		return ""
	}
	args, _, _ := strings.Cut(rest, "; else sudo -n docker ")
	return args
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func newDetectorWithRunner(host inventory.Host, r sshRunner) *Detector {
	return &Detector{host: host, runner: r}
}

// TestDetect_InventoryMissServiceFoundInDocker verifies that a non-zero exit
// on the inventory check does not abort the chain — the detector must fall
// through to the docker probe.
func TestDetect_InventoryMissServiceFoundInDocker(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{matchPrefix: "docker ps -a", exitCode: 0, stdout: "frameworks-foghorn|running|foghorn:v1"},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists {
		t.Fatalf("expected Exists=true after docker probe succeeded")
	}
	if state.Mode != "docker" || !state.Running {
		t.Fatalf("got mode=%q running=%v, want docker/true", state.Mode, state.Running)
	}
	if state.Version != "v1" {
		t.Fatalf("version=%q, want v1", state.Version)
	}
}

func TestDetect_TransportFailureIsNotReportedAsMissingService(t *testing.T) {
	for _, exitCode := range []int{-1, 255} {
		exitCode := exitCode
		t.Run(fmt.Sprintf("exit_%d", exitCode), func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{responses: []fakeResponse{{
				matchPrefix: "cat /etc/frameworks/inventory.json",
				exitCode:    exitCode,
				err:         errors.New("connection timed out"),
			}}}
			d := newDetectorWithRunner(inventory.Host{Name: "regional-eu-2", ExternalIP: "1.2.3.4", User: "root"}, r)

			state, err := d.Detect(context.Background(), "foredeck")
			if err == nil || !strings.Contains(err.Error(), "connection timed out") {
				t.Fatalf("Detect error = %v, want transport failure", err)
			}
			if state != nil {
				t.Fatalf("transport failure returned misleading state: %+v", state)
			}
		})
	}
}

func TestDetect_DockerRequiresExactContainerName(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{
				matchPrefix: "docker ps -a --filter name=frameworks-chatwoot ",
				exitCode:    0,
				stdout:      "frameworks-chatwoot|running|chatwoot/chatwoot:v3.14.0\nframeworks-chatwoot-worker|running|chatwoot/chatwoot:v3.14.0",
			},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "chatwoot")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Metadata["container_name"] != "frameworks-chatwoot" {
		t.Fatalf("container_name=%q", state.Metadata["container_name"])
	}
	if state.Version != "v3.14.0" {
		t.Fatalf("version=%q, want v3.14.0", state.Version)
	}
}

func TestDetect_DockerVersionFromDigestPinnedImage(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{
				matchPrefix: "docker ps -a --filter name=frameworks-nginx ",
				exitCode:    0,
				stdout:      "frameworks-nginx|running|nginx:1.29.3-alpine",
			},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true|nginx:1.29.3-alpine@sha256:abcdef"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "nginx")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Version != "1.29.3-alpine" {
		t.Fatalf("version=%q, want 1.29.3-alpine", state.Version)
	}
	if state.Metadata["image"] != "nginx:1.29.3-alpine@sha256:abcdef" {
		t.Fatalf("image=%q, want digest-pinned runtime reference", state.Metadata["image"])
	}
}

func TestDetect_DockerInspectsExactFallbackContainerName(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a --filter name=frameworks-foredeck ", exitCode: 0},
			{
				matchPrefix: "docker ps -a --filter name=foredeck ",
				exitCode:    0,
				stdout:      "foredeck|running|example/foredeck:v0.3.2",
			},
			{
				matchPrefix: "docker inspect -f '{{.State.Running}}|{{.Config.Image}}' 'foredeck'",
				exitCode:    0,
				stdout:      "true|example/foredeck:v0.3.2@sha256:abcdef",
			},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foredeck")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Metadata["container_name"] != "foredeck" || state.Metadata["image"] != "example/foredeck:v0.3.2@sha256:abcdef" {
		t.Fatalf("fallback container runtime identity = %+v", state.Metadata)
	}
}

func TestDetect_DockerProbesFallBackToSudo(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{
				matchPrefix: "docker ps -a --filter name=frameworks-chartroom ",
				exitCode:    0,
				stdout:      "frameworks-chartroom|running|livepeerframeworks/frameworks-chartroom:v0.3.11-rc1",
			},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true|livepeerframeworks/frameworks-chartroom:v0.3.11-rc1"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "deploy"}, r)

	state, err := d.Detect(context.Background(), "chartroom")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Mode != "docker" || state.Version != "v0.3.11-rc1" {
		t.Fatalf("mode=%q version=%q, want docker v0.3.11-rc1", state.Mode, state.Version)
	}
	var dockerCalls int
	for _, call := range r.calls {
		if strings.Contains(call, "docker ") {
			dockerCalls++
			if call != system.DockerCommand(dockerUserBranch(call)) {
				t.Fatalf("docker probe %q is not wrapped with the sudo fallback", call)
			}
		}
	}
	if dockerCalls != 2 {
		t.Fatalf("docker probes = %d, want ps and inspect", dockerCalls)
	}
}

func TestDetect_InventoryDockerCapturesPinnedImage(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{
				matchPrefix: "cat /etc/frameworks/inventory.json",
				exitCode:    0,
				stdout:      `{"services":{"foredeck":{"mode":"docker","version":"v0.3.0","provisioned_at":"2026-09-14T12:00:00Z"}}}`,
			},
			{
				matchPrefix: "docker inspect",
				exitCode:    0,
				stdout:      "true|livepeerframeworks/frameworks-foredeck:v0.3.0@sha256:abc123",
			},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foredeck")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Running || state.Metadata["image"] != "livepeerframeworks/frameworks-foredeck:v0.3.0@sha256:abc123" {
		t.Fatalf("inventory-backed Docker detection lost immutable image identity: %+v", state)
	}
}

func TestDockerImageVersion(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"foghorn:v0.2.69": "v0.2.69",
		"ghcr.io/livepeer-frameworks/foghorn:v0.2.69":        "v0.2.69",
		"nginx:1.29.3-alpine@sha256:abcdef":                  "1.29.3-alpine",
		"registry.local:5000/livepeer/foghorn:v0.2.69":       "v0.2.69",
		"registry.local:5000/livepeer/foghorn@sha256:abcdef": "",
		"registry.local:5000/livepeer/foghorn":               "",
		"sha256:abcdef":                                      "",
	}
	for image, want := range tests {
		if got := dockerImageVersion(image); got != want {
			t.Fatalf("dockerImageVersion(%q)=%q, want %q", image, got, want)
		}
	}
}

// TestDetect_AllMethodsFailReturnsNotFound verifies that exhaustively failing
// probes yield Exists=false rather than bubbling up an error.
func TestDetect_AllMethodsFailReturnsNotFound(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "", exitCode: 1, stderr: "fail"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "some-service")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Exists {
		t.Fatalf("expected Exists=false when every probe returned non-zero")
	}
}

// TestDetect_NonZeroExitIsNotHardFailure pins the contract: a non-zero exit
// from one method must NOT short-circuit; the detector moves to the next.
func TestDetect_NonZeroExitIsNotHardFailure(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a", exitCode: 1},
			{matchPrefix: "systemctl show", exitCode: 0, stdout: "LoadState=loaded\nActiveState=active\nSubState=running"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists || state.Mode != "native" || !state.Running {
		t.Fatalf("got %+v, want native service detected as running", state)
	}
	// Chain must have progressed past inventory and docker to systemd.
	if len(r.calls) < 3 {
		t.Fatalf("expected the detector to try multiple methods, got %d calls", len(r.calls))
	}
}

func TestDetect_SystemdReadsNativePlatformVersion(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a", exitCode: 1},
			{
				matchPrefix: "systemctl show",
				exitCode:    0,
				stdout: strings.Join([]string{
					"LoadState=loaded",
					"ActiveState=active",
					"SubState=running",
					"User=frameworks",
					"WorkingDirectory=/opt/frameworks/quartermaster",
					"EnvironmentFiles=/etc/frameworks/quartermaster.env (ignore_errors=no)",
					"ExecStart={ path=/opt/frameworks/quartermaster/quartermaster ; argv[]=/opt/frameworks/quartermaster/quartermaster serve ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }",
				}, "\n"),
			},
			{
				matchPrefix: "'/opt/frameworks/quartermaster/quartermaster' version --json",
				exitCode:    0,
				stdout:      `{"version":"v0.2.32","component_name":"quartermaster","component_version":"0.2.0"}`,
			},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "quartermaster")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Version != "v0.2.32" {
		t.Fatalf("version=%q, want v0.2.32", state.Version)
	}
	if state.Metadata["binary_path"] != "/opt/frameworks/quartermaster/quartermaster" {
		t.Fatalf("binary_path=%q", state.Metadata["binary_path"])
	}
	if state.Metadata["environment_file"] != "/etc/frameworks/quartermaster.env" {
		t.Fatalf("environment_file=%q", state.Metadata["environment_file"])
	}
	if state.Metadata["working_directory"] != "/opt/frameworks/quartermaster" {
		t.Fatalf("working_directory=%q", state.Metadata["working_directory"])
	}
	if state.Metadata["service_user"] != "frameworks" {
		t.Fatalf("service_user=%q", state.Metadata["service_user"])
	}
}

func TestDetect_SystemdManagedComposeStackReportsDockerMode(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{responses: []fakeResponse{
		{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
		{matchPrefix: "docker ps -a", exitCode: 1},
		{matchPrefix: "systemctl show", exitCode: 0, stdout: strings.Join([]string{
			"LoadState=loaded",
			"ActiveState=active",
			"SubState=exited",
			"ExecStart={ path=/usr/bin/docker ; argv[]=/usr/bin/docker compose -f /opt/frameworks/grafana/docker-compose.yml up -d ; }",
		}, "\n")},
	}}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "grafana")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists || !state.Running || state.Mode != "docker" {
		t.Fatalf("systemd-managed compose stack = %+v, want running docker", state)
	}
}

func TestSystemdEnvironmentFile(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"/etc/frameworks/commodore.env (ignore_errors=no)":  "/etc/frameworks/commodore.env",
		"-/etc/frameworks/optional.env (ignore_errors=yes)": "/etc/frameworks/optional.env",
		"": "",
	} {
		if got := systemdEnvironmentFile(input); got != want {
			t.Fatalf("systemdEnvironmentFile(%q)=%q, want %q", input, got, want)
		}
	}
}
